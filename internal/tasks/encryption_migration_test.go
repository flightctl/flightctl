package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type memoryCheckpointStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemoryCheckpointStore() *memoryCheckpointStore {
	return &memoryCheckpointStore{data: make(map[string][]byte)}
}

func (s *memoryCheckpointStore) key(consumer, key string) string {
	return consumer + "/" + key
}

func (s *memoryCheckpointStore) Set(_ context.Context, consumer string, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, len(value))
	copy(cp, value)
	s.data[s.key(consumer, key)] = cp
	return nil
}

func (s *memoryCheckpointStore) Get(_ context.Context, consumer string, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.data[s.key(consumer, key)]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	return cp, nil
}

func (s *memoryCheckpointStore) GetDatabaseTime(context.Context) (time.Time, error) {
	return time.Now().UTC(), nil
}

type fakeRow struct {
	orgID   uuid.UUID
	name    string
	changed bool
	fail    bool
	keyIDs  []string
}

func (r *fakeRow) OrgID() uuid.UUID { return r.orgID }
func (r *fakeRow) Name() string     { return r.name }

func (r *fakeRow) Migrate(context.Context, encryption.EncryptFunc) (bool, []string, error) {
	if r.fail {
		return false, nil, fmt.Errorf("migrate failed")
	}
	return r.changed, r.keyIDs, nil
}

func (r *fakeRow) Persist(context.Context) error {
	if r.fail {
		return fmt.Errorf("persist failed")
	}
	return nil
}

type fakeResource struct {
	kind  string
	rows  []EncryptionMigratableRow
	paths [][]string
}

func (r *fakeResource) Kind() string { return r.kind }

func (r *fakeResource) EncryptPaths() [][]string {
	if len(r.paths) == 0 {
		return [][]string{{"fake"}}
	}
	return r.paths
}

func (r *fakeResource) NextPage(_ context.Context, orgID uuid.UUID, afterName string, limit int) ([]EncryptionMigratableRow, error) {
	out := make([]EncryptionMigratableRow, 0, limit)
	for _, row := range r.rows {
		if row.OrgID() != orgID {
			continue
		}
		if afterName != "" && row.Name() <= afterName {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func newLocalEncryptionManager(t *testing.T) *encryption.Manager {
	t.Helper()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "key")
	key, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0600))

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "default", Path: keyPath}},
		ActiveKeyID: "default",
	}
	v1, err := encryption.NewV1Strategy(cfg)
	require.NoError(t, err)
	mgr := encryption.NewManager()
	mgr.RegisterStrategy(v1, true)
	return mgr
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{
		Logger:                                   logger.Default.LogMode(logger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err)
	return db
}

func newTestMigrator(t *testing.T, mgr *encryption.Manager, checkpoints CheckpointStore) *EncryptionMigrator {
	t.Helper()
	db := openTestDB(t)
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	return NewEncryptionMigrator(db, mgr, checkpoints, nil, log)
}

func TestEncryptionMigrator_WhenPageEmptyItShouldMarkComplete(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{org})
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: nil},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.True(t, report.Complete)
	assert.Equal(t, 0, report.Scanned)

	needs, err := migrator.NeedsMigration(context.Background())
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestEncryptionMigrator_WhenRowsNeedUpdateItShouldAdvanceCheckpoint(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.SetBatchSize(2)
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
			&fakeRow{orgID: org, name: "b", changed: false, keyIDs: []string{"default"}},
			&fakeRow{orgID: org, name: "c", changed: true, keyIDs: []string{"default"}},
		}},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 2, report.Scanned)
	assert.Equal(t, 1, report.Updated)
	assert.Equal(t, 1, report.Unchanged)
	assert.Equal(t, []string{"default"}, report.KeyIDsInUse)

	report, err = migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, 1, report.Updated)

	report, err = migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.True(t, report.Complete)
}

func TestEncryptionMigrator_WhenRowFailsItShouldContinue(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "bad", fail: true},
			&fakeRow{orgID: org, name: "good", changed: true, keyIDs: []string{"default"}},
		}},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.Equal(t, 2, report.Scanned)
	assert.Equal(t, 1, report.Errors)
	assert.Equal(t, 1, report.Updated)
	assert.False(t, report.Complete)

	raw, err := checkpoints.Get(context.Background(), encryptionMigrationConsumer, checkpointKey("Fake", org))
	require.NoError(t, err)
	var checkpoint EncryptionMigrationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &checkpoint))
	assert.True(t, checkpoint.PassHadErrors)
	assert.Equal(t, "good", checkpoint.LastName)
}

func TestEncryptionMigrator_WhenPassHadErrorsItShouldBackoffBeforeRestart(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "bad", fail: true},
		}},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Errors)
	assert.False(t, report.Complete)

	report, err = migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, defaultEncryptionMigrationErrorBackoff, report.RetryAfter)

	raw, err := checkpoints.Get(context.Background(), encryptionMigrationConsumer, checkpointKey("Fake", org))
	require.NoError(t, err)
	var checkpoint EncryptionMigrationCheckpoint
	require.NoError(t, json.Unmarshal(raw, &checkpoint))
	assert.False(t, checkpoint.PassHadErrors)
	assert.Equal(t, "", checkpoint.LastName)
	assert.False(t, checkpoint.Complete)
	require.NotNil(t, checkpoint.BackoffUntil)
	assert.True(t, checkpoint.BackoffUntil.After(time.Now().UTC()))

	report, err = migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 0, report.Scanned)
	assert.Greater(t, report.RetryAfter, time.Duration(0))
}

func TestEncryptionMigrator_WhenActiveKeyChangesItShouldReset(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		}},
	}
	require.NoError(t, checkpoints.Set(context.Background(), encryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID:       "old-key",
		EncryptPathsFingerprint: "fake",
		Complete:                true,
		LastName:                "a",
	})))

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, "default", report.ActiveKeyID)
}

func TestEncryptionMigrator_WhenEncryptPathsChangeItShouldReset(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{org})
	resource := &fakeResource{
		kind:  "Fake",
		paths: [][]string{{"secret"}},
		rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		},
	}
	migrator.resources = map[string]EncryptionMigrationResource{"Fake": resource}
	require.NoError(t, checkpoints.Set(context.Background(), encryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID:       "default",
		EncryptPathsFingerprint: "secret",
		Complete:                true,
		LastName:                "a",
	})))

	resource.paths = [][]string{{"secret"}, {"token"}}
	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 1, report.Scanned)

	needs, err := migrator.NeedsMigration(context.Background())
	require.NoError(t, err)
	assert.True(t, needs)
}

func TestEncryptionMigrator_WhenMultipleOrgsItShouldIsolateWork(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	orgA := uuid.New()
	orgB := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: orgA, name: "a", changed: true, keyIDs: []string{"default"}},
			&fakeRow{orgID: orgB, name: "b", changed: true, keyIDs: []string{"default"}},
		}},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", orgA)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, 1, report.Updated)

	report, err = migrator.RunBatch(context.Background(), "Fake", orgA)
	require.NoError(t, err)
	assert.True(t, report.Complete)

	report, err = migrator.RunBatch(context.Background(), "Fake", orgB)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Updated)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestRepositoryEncryptionMigration_EndToEnd(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	db := openTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE repositories (
		org_id TEXT NOT NULL,
		name TEXT NOT NULL,
		resource_version INTEGER,
		deleted_at DATETIME,
		spec TEXT,
		PRIMARY KEY (org_id, name)
	)`).Error)

	org := uuid.New()
	var spec domain.RepositorySpec
	require.NoError(t, spec.FromGitRepoSpec(domain.GitRepoSpec{
		Url: "https://example.com/repo.git",
		HttpConfig: &domain.HttpConfig{
			Username: strPtr("user"),
			Password: strPtr("super-secret"),
		},
	}))
	repo := model.Repository{
		Resource: model.Resource{OrgID: org, Name: "repo-1"},
		Spec:     model.MakeJSONField(spec),
	}
	require.NoError(t, db.Table("repositories").Create(map[string]any{
		"org_id":           org,
		"name":             "repo-1",
		"resource_version": 1,
		"spec":             mustJSON(t, spec),
	}).Error)

	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	migrator := NewEncryptionMigrator(db, mgr, newMemoryCheckpointStore(), nil, log)
	migrator.SetBatchSize(10)

	report, err := migrator.RunBatch(context.Background(), domain.RepositoryKind, org)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, 1, report.Updated)
	assert.Contains(t, report.KeyIDsInUse, "default")

	var stored model.Repository
	require.NoError(t, db.First(&stored, "org_id = ? AND name = ?", org, "repo-1").Error)
	_ = repo
	storedJSON, err := json.Marshal(stored.Spec.Data)
	require.NoError(t, err)
	assert.NotContains(t, string(storedJSON), "super-secret")
	assert.Contains(t, string(storedJSON), "enc:v1:default:")

	report, err = migrator.RunBatch(context.Background(), domain.RepositoryKind, org)
	require.NoError(t, err)
	if !report.Complete {
		report, err = migrator.RunBatch(context.Background(), domain.RepositoryKind, org)
		require.NoError(t, err)
	}
	assert.True(t, report.Complete)
}

func TestPersistMigratedSpec_WhenResourceVersionConflictsItShouldError(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE repositories (
		org_id TEXT NOT NULL,
		name TEXT NOT NULL,
		resource_version INTEGER,
		deleted_at DATETIME,
		spec TEXT,
		PRIMARY KEY (org_id, name)
	)`).Error)

	org := uuid.New()
	var spec domain.RepositorySpec
	require.NoError(t, spec.FromGitRepoSpec(domain.GitRepoSpec{
		Url: "https://example.com/repo.git",
		HttpConfig: &domain.HttpConfig{
			Username: strPtr("user"),
			Password: strPtr("password"),
		},
	}))
	require.NoError(t, db.Table("repositories").Create(map[string]any{
		"org_id":           org,
		"name":             "repo-1",
		"resource_version": 2,
		"spec":             mustJSON(t, spec),
	}).Error)

	staleVersion := int64(1)
	row := &model.Repository{
		Resource: model.Resource{OrgID: org, Name: "repo-1", ResourceVersion: &staleVersion},
		Spec:     model.MakeJSONField(spec),
	}
	err := persistMigratedSpec(context.Background(), db, row, org, "repo-1", &staleVersion, row.Spec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent update")
}

func TestAuthProviderEncryptionMigration_EndToEnd(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	db := openTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE auth_providers (
		org_id TEXT NOT NULL,
		name TEXT NOT NULL,
		resource_version INTEGER,
		deleted_at DATETIME,
		spec TEXT,
		PRIMARY KEY (org_id, name)
	)`).Error)

	org := uuid.New()
	var spec domain.AuthProviderSpec
	require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
		ClientId:               "client",
		ClientSecret:           "auth-secret",
		Issuer:                 "https://issuer.example.com",
		ProviderType:           domain.Oidc,
		OrganizationAssignment: staticOrgAssignment(),
	}))
	require.NoError(t, db.Table("auth_providers").Create(map[string]any{
		"org_id":           org,
		"name":             "oidc-1",
		"resource_version": 1,
		"spec":             mustJSON(t, spec),
	}).Error)

	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	migrator := NewEncryptionMigrator(db, mgr, newMemoryCheckpointStore(), nil, log)

	report, err := migrator.RunBatch(context.Background(), domain.AuthProviderKind, org)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Updated)

	var stored model.AuthProvider
	require.NoError(t, db.First(&stored, "org_id = ? AND name = ?", org, "oidc-1").Error)
	storedJSON, err := json.Marshal(stored.Spec.Data)
	require.NoError(t, err)
	assert.NotContains(t, string(storedJSON), "auth-secret")
	assert.Contains(t, string(storedJSON), "enc:v1:default:")
}

func staticOrgAssignment() domain.AuthOrganizationAssignment {
	var oa domain.AuthOrganizationAssignment
	_ = oa.FromAuthStaticOrganizationAssignment(domain.AuthStaticOrganizationAssignment{
		Type: domain.AuthStaticOrganizationAssignmentTypeStatic,
	})
	return oa
}

type recordingPublisher struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (p *recordingPublisher) Enqueue(_ context.Context, payload []byte, _ int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	p.payloads = append(p.payloads, cp)
	return nil
}

func (p *recordingPublisher) Close() {}

func (p *recordingPublisher) workItems() []EncryptionMigrationWork {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]EncryptionMigrationWork, 0, len(p.payloads))
	for _, payload := range p.payloads {
		var event worker_client.EventWithOrgId
		if err := json.Unmarshal(payload, &event); err != nil {
			continue
		}
		out = append(out, EncryptionMigrationWork{
			Kind:  event.Event.InvolvedObject.Kind,
			OrgID: event.OrgId,
		})
	}
	return out
}

func TestEnqueueEncryptionMigrationIfNeeded_WhenIncompleteItShouldEnqueueOrgs(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	migrator := newTestMigrator(t, mgr, newMemoryCheckpointStore())
	orgA := uuid.New()
	orgB := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{orgA, orgB})
	migrator.resources = map[string]EncryptionMigrationResource{
		domain.RepositoryKind:   &fakeResource{kind: domain.RepositoryKind},
		domain.AuthProviderKind: &fakeResource{kind: domain.AuthProviderKind},
	}
	publisher := &recordingPublisher{}

	require.NoError(t, EnqueueEncryptionMigrationIfNeeded(context.Background(), publisher, migrator, logrus.New()))
	assert.ElementsMatch(t, []EncryptionMigrationWork{
		{Kind: domain.RepositoryKind, OrgID: orgA},
		{Kind: domain.RepositoryKind, OrgID: orgB},
		{Kind: domain.AuthProviderKind, OrgID: orgA},
		{Kind: domain.AuthProviderKind, OrgID: orgB},
	}, publisher.workItems())
}

func TestEnqueueEncryptionMigrationIfNeeded_WhenCompleteItShouldEnqueueNothing(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{org})
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake"},
	}
	require.NoError(t, checkpoints.Set(context.Background(), encryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID:       "default",
		EncryptPathsFingerprint: "fake",
		Complete:                true,
	})))
	publisher := &recordingPublisher{}

	require.NoError(t, EnqueueEncryptionMigrationIfNeeded(context.Background(), publisher, migrator, logrus.New()))
	assert.Empty(t, publisher.workItems())
}

func TestRunEncryptionMigrationBatch_WhenIncompleteItShouldSelfChain(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		}},
	}
	publisher := &recordingPublisher{}
	event := domain.Event{
		Reason:         EventReasonEncryptionMigrationBatch,
		InvolvedObject: domain.ObjectReference{Kind: "Fake", Name: encryptionMigrationResourceName},
	}

	require.NoError(t, runEncryptionMigrationBatch(context.Background(), org, event, migrator, publisher, logrus.New()))
	assert.Equal(t, []EncryptionMigrationWork{{Kind: "Fake", OrgID: org}}, publisher.workItems())
}

func TestRunEncryptionMigrationBatch_WhenCompleteItShouldNotSelfChain(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake"},
	}
	publisher := &recordingPublisher{}
	event := domain.Event{
		Reason:         EventReasonEncryptionMigrationBatch,
		InvolvedObject: domain.ObjectReference{Kind: "Fake", Name: encryptionMigrationResourceName},
	}

	require.NoError(t, runEncryptionMigrationBatch(context.Background(), org, event, migrator, publisher, logrus.New()))
	assert.Empty(t, publisher.workItems())
}

func TestRunEncryptionMigrationBatch_WhenRetryAfterItShouldDelayEnqueue(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	migrator.SetErrorBackoff(100 * time.Millisecond)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "bad", fail: true},
		}},
	}
	// First batch records the error; second ends the pass and sets backoff.
	_, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)

	publisher := &recordingPublisher{}
	event := domain.Event{
		Reason:         EventReasonEncryptionMigrationBatch,
		InvolvedObject: domain.ObjectReference{Kind: "Fake", Name: encryptionMigrationResourceName},
	}
	require.NoError(t, runEncryptionMigrationBatch(context.Background(), org, event, migrator, publisher, logrus.New()))
	assert.Empty(t, publisher.workItems(), "backoff should not enqueue immediately")

	require.Eventually(t, func() bool {
		return len(publisher.workItems()) == 1
	}, time.Second, 20*time.Millisecond)
	assert.Equal(t, []EncryptionMigrationWork{{Kind: "Fake", OrgID: org}}, publisher.workItems())
}

type blockingLocker struct {
	mu       sync.Mutex
	held     map[string]struct{}
	tryCount int
}

func newBlockingLocker() *blockingLocker {
	return &blockingLocker{held: make(map[string]struct{})}
}

func (l *blockingLocker) TryLock(_ context.Context, key string) (func() error, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tryCount++
	if _, ok := l.held[key]; ok {
		return nil, false, nil
	}
	l.held[key] = struct{}{}
	return func() error {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.held, key)
		return nil
	}, true, nil
}

func TestRunEncryptionMigrationBatch_WhenLeaseBusyItShouldSkip(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		}},
	}
	locker := newBlockingLocker()
	unlock, ok, err := locker.TryLock(context.Background(), leaseKey("Fake", org))
	require.NoError(t, err)
	require.True(t, ok)
	migrator.locker = locker

	publisher := &recordingPublisher{}
	event := domain.Event{
		Reason:         EventReasonEncryptionMigrationBatch,
		InvolvedObject: domain.ObjectReference{Kind: "Fake", Name: encryptionMigrationResourceName},
	}
	require.NoError(t, runEncryptionMigrationBatch(context.Background(), org, event, migrator, publisher, logrus.New()))
	assert.Empty(t, publisher.workItems())
	require.NoError(t, unlock())
}

func TestRunEncryptionMigrationBatch_WhenLeaseAvailableItShouldRun(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		}},
	}
	migrator.locker = newBlockingLocker()
	publisher := &recordingPublisher{}
	event := domain.Event{
		Reason:         EventReasonEncryptionMigrationBatch,
		InvolvedObject: domain.ObjectReference{Kind: "Fake", Name: encryptionMigrationResourceName},
	}
	require.NoError(t, runEncryptionMigrationBatch(context.Background(), org, event, migrator, publisher, logrus.New()))
	assert.Equal(t, []EncryptionMigrationWork{{Kind: "Fake", OrgID: org}}, publisher.workItems())
}

func TestEncryptionMigrationAdvisoryKeys_WhenKeysDifferTheyShouldDiffer(t *testing.T) {
	orgA := uuid.New()
	orgB := uuid.New()
	_, kRepo := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgA))
	_, kAP := encryptionMigrationAdvisoryKeys(leaseKey(domain.AuthProviderKind, orgA))
	assert.NotEqual(t, kRepo, kAP)

	_, kOrgA := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgA))
	_, kOrgB := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgB))
	assert.NotEqual(t, kOrgA, kOrgB)
}

func TestCollectKeyIDsFromSpec_WhenPathsMatchItShouldReturnKeyIDs(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	encrypted, err := mgr.ProcessEncryption(context.Background(), []byte("secret"))
	require.NoError(t, err)

	spec := map[string]any{
		"httpConfig": map[string]any{
			"password": string(encrypted),
			"token":    "",
		},
	}
	keyIDs, err := collectKeyIDsFromSpec(spec, [][]string{
		{"httpConfig", "password"},
		{"httpConfig", "token"},
		{"missing", "path"},
	}, mgr)
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, keyIDs)
}

func TestCollectKeyIDsFromSpec_WhenLeafIsNotStringItShouldSkip(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	keyIDs, err := collectKeyIDsFromSpec(map[string]any{
		"httpConfig": map[string]any{
			"password": 123,
		},
	}, [][]string{{"httpConfig", "password"}}, mgr)
	require.NoError(t, err)
	assert.Empty(t, keyIDs)
}
