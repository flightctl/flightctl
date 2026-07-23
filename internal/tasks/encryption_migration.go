package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

const (
	encryptionMigrationConsumer            = "encryption-migration"
	encryptionMigrationResourceName        = "batch"
	defaultEncryptionBatchSize             = 100
	defaultEncryptionMigrationErrorBackoff = 30 * time.Second
	encryptionMigrationOrgPageSize         = 100
)

// EncryptionMigrationCheckpoint tracks scanner progress for one kind within one org.
type EncryptionMigrationCheckpoint struct {
	TargetActiveKeyID string `json:"targetActiveKeyId"`
	// EncryptPathsFingerprint is the sorted protected-field path list used for this pass.
	// When application versions add/remove encrypt paths, a mismatch forces a rescan.
	EncryptPathsFingerprint string `json:"encryptPathsFingerprint,omitempty"`
	LastName                string `json:"lastName"`
	Complete                bool   `json:"complete"`
	// PassHadErrors is set when any row fails during the current scan pass.
	// Complete is only allowed after a full pass with no errors.
	PassHadErrors bool `json:"passHadErrors,omitempty"`
	// BackoffUntil delays the next scan after a pass that had errors.
	BackoffUntil *time.Time `json:"backoffUntil,omitempty"`
}

// EncryptionMigrationReport summarizes one batch for a kind within one org.
type EncryptionMigrationReport struct {
	Kind        string
	OrgID       uuid.UUID
	Scanned     int
	Updated     int
	Unchanged   int
	Errors      int
	KeyIDsInUse []string
	ActiveKeyID string
	Complete    bool
	// RetryAfter asks the consumer to delay before re-enqueueing.
	RetryAfter time.Duration
}

// EncryptionMigrationWork identifies one per-org migration unit.
type EncryptionMigrationWork struct {
	Kind  string
	OrgID uuid.UUID
}

// EncryptionMigrationResource pages and migrates one resource kind within an org.
type EncryptionMigrationResource interface {
	Kind() string
	// EncryptPaths returns protected JSON paths for this kind.
	// Production adapters must delegate to model.EncryptPathsForKind / EncryptionFieldPaths
	// (the single source of truth); do not maintain a parallel path list here.
	EncryptPaths() [][]string
	NextPage(ctx context.Context, orgID uuid.UUID, afterName string, limit int) ([]EncryptionMigratableRow, error)
}

// EncryptionMigratableRow is one persisted resource to reprocess.
type EncryptionMigratableRow interface {
	OrgID() uuid.UUID
	Name() string
	Migrate(ctx context.Context, encrypt encryption.EncryptFunc) (changed bool, keyIDs []string, err error)
	Persist(ctx context.Context) error
}

// EncryptionMigrator runs batched encryption migration for registered kinds.
type EncryptionMigrator struct {
	db           *gorm.DB
	manager      *encryption.Manager
	checkpoints  CheckpointStore
	locker       EncryptionMigrationLocker
	log          logrus.FieldLogger
	batchSize    int
	errorBackoff time.Duration
	resources    map[string]EncryptionMigrationResource
	// orgs overrides organization discovery in tests.
	orgs []uuid.UUID
}

// NewEncryptionMigrator builds a migrator with Repository and AuthProvider adapters.
// Pass nil locker to disable cross-replica leasing (tests).
func NewEncryptionMigrator(db *gorm.DB, manager *encryption.Manager, checkpoints CheckpointStore, locker EncryptionMigrationLocker, log logrus.FieldLogger) *EncryptionMigrator {
	if locker == nil {
		locker = noopEncryptionMigrationLocker{}
	}
	m := &EncryptionMigrator{
		db:           db,
		manager:      manager,
		checkpoints:  checkpoints,
		locker:       locker,
		log:          log,
		batchSize:    defaultEncryptionBatchSize,
		errorBackoff: defaultEncryptionMigrationErrorBackoff,
		resources:    make(map[string]EncryptionMigrationResource),
	}
	m.RegisterResource(newRepositoryEncryptionResource(db, manager))
	m.RegisterResource(newAuthProviderEncryptionResource(db, manager))
	return m
}

// RegisterResource adds or replaces a kind adapter (for tests or future kinds).
func (m *EncryptionMigrator) RegisterResource(resource EncryptionMigrationResource) {
	m.resources[resource.Kind()] = resource
}

// SetBatchSize overrides the default page size (tests / tuning).
func (m *EncryptionMigrator) SetBatchSize(n int) {
	if n > 0 {
		m.batchSize = n
	}
}

// SetErrorBackoff overrides the delay before restarting a failed scan pass.
func (m *EncryptionMigrator) SetErrorBackoff(d time.Duration) {
	if d > 0 {
		m.errorBackoff = d
	}
}

// SetOrganizations overrides org discovery (tests).
func (m *EncryptionMigrator) SetOrganizations(orgs []uuid.UUID) {
	m.orgs = orgs
}

// RunBatch migrates one page for the given kind within one org.
func (m *EncryptionMigrator) RunBatch(ctx context.Context, kind string, orgID uuid.UUID) (EncryptionMigrationReport, error) {
	report := EncryptionMigrationReport{Kind: kind, OrgID: orgID}

	resource, ok := m.resources[kind]
	if !ok {
		return report, fmt.Errorf("encryption migration: unsupported kind %q", kind)
	}
	if m.manager == nil {
		return report, fmt.Errorf("encryption migration: encryption manager is nil")
	}

	_, strategy := m.manager.GetActiveStrategy()
	if strategy == nil {
		return report, encryption.ErrNoActiveStrategy
	}
	activeKeyID := strategy.ActiveKeyID()
	report.ActiveKeyID = activeKeyID
	pathsFingerprint := encryptPathsFingerprint(resource.EncryptPaths())

	checkpoint, err := m.loadCheckpoint(ctx, kind, orgID)
	if err != nil {
		return report, err
	}
	if !checkpointMatchesMigrationTarget(checkpoint, activeKeyID, pathsFingerprint) {
		if checkpoint.TargetActiveKeyID != "" || checkpoint.EncryptPathsFingerprint != "" {
			m.log.Infof("encryption migration: target changed for %s org %s (key %q->%q, paths %q->%q); resetting checkpoint",
				kind, orgID, checkpoint.TargetActiveKeyID, activeKeyID, checkpoint.EncryptPathsFingerprint, pathsFingerprint)
		}
		checkpoint = EncryptionMigrationCheckpoint{
			TargetActiveKeyID:       activeKeyID,
			EncryptPathsFingerprint: pathsFingerprint,
		}
	}
	if checkpoint.Complete {
		report.Complete = true
		return report, nil
	}
	if checkpoint.BackoffUntil != nil {
		remaining := time.Until(*checkpoint.BackoffUntil)
		if remaining > 0 {
			report.RetryAfter = remaining
			m.log.Infof("encryption migration: %s org %s in error backoff for %s", kind, orgID, remaining.Round(time.Second))
			return report, nil
		}
		checkpoint.BackoffUntil = nil
	}

	rows, err := resource.NextPage(ctx, orgID, checkpoint.LastName, m.batchSize)
	if err != nil {
		return report, fmt.Errorf("encryption migration: list %s org %s: %w", kind, orgID, err)
	}
	if len(rows) == 0 {
		if checkpoint.PassHadErrors {
			backoffUntil := time.Now().UTC().Add(m.errorBackoff)
			m.log.Warnf("encryption migration: %s org %s finished pass with errors; backing off until %s before restart for active key %q",
				kind, orgID, backoffUntil.Format(time.RFC3339), activeKeyID)
			checkpoint = EncryptionMigrationCheckpoint{
				TargetActiveKeyID:       activeKeyID,
				EncryptPathsFingerprint: pathsFingerprint,
				BackoffUntil:            &backoffUntil,
			}
			if err := m.saveCheckpoint(ctx, kind, orgID, checkpoint); err != nil {
				return report, err
			}
			report.RetryAfter = m.errorBackoff
			return report, nil
		}
		checkpoint.Complete = true
		checkpoint.TargetActiveKeyID = activeKeyID
		checkpoint.EncryptPathsFingerprint = pathsFingerprint
		checkpoint.PassHadErrors = false
		if err := m.saveCheckpoint(ctx, kind, orgID, checkpoint); err != nil {
			return report, err
		}
		report.Complete = true
		m.log.Infof("encryption migration: %s org %s complete for active key %q paths %q", kind, orgID, activeKeyID, pathsFingerprint)
		return report, nil
	}

	keyIDSet := map[string]struct{}{}
	for _, row := range rows {
		report.Scanned++
		// Always advance the cursor so a later success cannot skip a failed row forever.
		checkpoint.LastName = row.Name()

		changed, keyIDs, migrateErr := row.Migrate(ctx, m.manager.ProcessEncryption)
		if migrateErr != nil {
			report.Errors++
			checkpoint.PassHadErrors = true
			m.log.WithError(migrateErr).Errorf("encryption migration: migrate %s org %s/%s",
				kind, orgID, row.Name())
			continue
		}
		if !changed {
			report.Unchanged++
			addKeyIDs(keyIDSet, keyIDs)
			continue
		}
		if err := row.Persist(ctx); err != nil {
			report.Errors++
			checkpoint.PassHadErrors = true
			m.log.WithError(err).Errorf("encryption migration: persist %s org %s/%s",
				kind, orgID, row.Name())
			continue
		}
		report.Updated++
		addKeyIDs(keyIDSet, keyIDs)
	}

	checkpoint.TargetActiveKeyID = activeKeyID
	checkpoint.EncryptPathsFingerprint = pathsFingerprint
	checkpoint.Complete = false
	if err := m.saveCheckpoint(ctx, kind, orgID, checkpoint); err != nil {
		return report, err
	}

	report.KeyIDsInUse = sortedKeys(keyIDSet)
	m.log.Infof("encryption migration: %s org %s batch scanned=%d updated=%d unchanged=%d errors=%d keysInUse=%v",
		kind, orgID, report.Scanned, report.Updated, report.Unchanged, report.Errors, report.KeyIDsInUse)
	return report, nil
}

// NeedsMigration reports whether any kind/org still needs work for the active key.
func (m *EncryptionMigrator) NeedsMigration(ctx context.Context) (bool, error) {
	work, err := m.IncompleteWork(ctx)
	if err != nil {
		return false, err
	}
	return len(work) > 0, nil
}

func (m *EncryptionMigrator) forEachOrgID(ctx context.Context, fn func(uuid.UUID) error) error {
	if m.orgs != nil {
		for _, orgID := range m.orgs {
			if err := fn(orgID); err != nil {
				return err
			}
		}
		return nil
	}

	// org.DefaultID is uuid.Nil, so pagination cannot use Nil as a "no cursor" sentinel.
	var after uuid.UUID
	hasAfter := false
	for {
		var ids []uuid.UUID
		q := m.db.WithContext(ctx).Model(&model.Organization{}).Order("id ASC").Limit(encryptionMigrationOrgPageSize)
		if hasAfter {
			q = q.Where("id > ?", after)
		}
		if err := q.Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("encryption migration: list organizations: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		for _, orgID := range ids {
			if err := fn(orgID); err != nil {
				return err
			}
		}
		after = ids[len(ids)-1]
		hasAfter = true
		if len(ids) < encryptionMigrationOrgPageSize {
			return nil
		}
	}
}

func addKeyIDs(set map[string]struct{}, keyIDs []string) {
	for _, id := range keyIDs {
		if id != "" {
			set[id] = struct{}{}
		}
	}
}

func checkpointKey(kind string, orgID uuid.UUID) string {
	return fmt.Sprintf("%s:%s", kind, orgID)
}

func leaseKey(kind string, orgID uuid.UUID) string {
	return fmt.Sprintf("%s:%s", kind, orgID)
}

// encryptPathsFingerprint returns a stable, readable fingerprint of protected JSON paths.
func encryptPathsFingerprint(paths [][]string) string {
	parts := make([]string, 0, len(paths))
	for _, path := range paths {
		if len(path) == 0 {
			continue
		}
		parts = append(parts, strings.Join(path, "."))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func checkpointMatchesMigrationTarget(checkpoint EncryptionMigrationCheckpoint, activeKeyID, pathsFingerprint string) bool {
	return checkpoint.TargetActiveKeyID == activeKeyID && checkpoint.EncryptPathsFingerprint == pathsFingerprint
}

func (m *EncryptionMigrator) pathsFingerprintForKind(kind string) (string, error) {
	resource, ok := m.resources[kind]
	if !ok {
		return "", fmt.Errorf("encryption migration: unsupported kind %q", kind)
	}
	return encryptPathsFingerprint(resource.EncryptPaths()), nil
}

func (m *EncryptionMigrator) loadCheckpoint(ctx context.Context, kind string, orgID uuid.UUID) (EncryptionMigrationCheckpoint, error) {
	data, err := m.checkpoints.Get(ctx, encryptionMigrationConsumer, checkpointKey(kind, orgID))
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			return EncryptionMigrationCheckpoint{}, nil
		}
		return EncryptionMigrationCheckpoint{}, fmt.Errorf("load checkpoint for %s org %s: %w", kind, orgID, err)
	}
	var checkpoint EncryptionMigrationCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return EncryptionMigrationCheckpoint{}, fmt.Errorf("decode checkpoint for %s org %s: %w", kind, orgID, err)
	}
	return checkpoint, nil
}

func (m *EncryptionMigrator) saveCheckpoint(ctx context.Context, kind string, orgID uuid.UUID, checkpoint EncryptionMigrationCheckpoint) error {
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode checkpoint for %s org %s: %w", kind, orgID, err)
	}
	if err := m.checkpoints.Set(ctx, encryptionMigrationConsumer, checkpointKey(kind, orgID), data); err != nil {
		return fmt.Errorf("save checkpoint for %s org %s: %w", kind, orgID, err)
	}
	return nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func collectKeyIDsFromSpec(data any, paths [][]string, mgr *encryption.Manager) ([]string, error) {
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var root map[string]any
	if err := json.Unmarshal(jsonBytes, &root); err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	for _, path := range paths {
		val, ok := jsonStringAtPath(root, path)
		if !ok || val == "" {
			continue
		}
		_, keyID, encrypted, err := encryption.InspectEncrypted([]byte(val), mgr)
		if err != nil {
			return nil, err
		}
		if encrypted && keyID != "" {
			seen[keyID] = struct{}{}
		}
	}
	return sortedKeys(seen), nil
}

func jsonStringAtPath(data map[string]any, path []string) (string, bool) {
	current := any(data)
	for _, part := range path {
		asMap, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		next, exists := asMap[part]
		if !exists {
			return "", false
		}
		current = next
	}
	str, ok := current.(string)
	return str, ok
}
