package updatercatalog

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrStaleRevision    = errors.New("updater catalog revision is stale")
	ErrRevisionConflict = errors.New("updater catalog revision has different input")
	ErrHistoryConflict  = errors.New("updater catalog conflicts with retained history")
	digestPattern       = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	identityPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
)

const (
	maxRetainedReleases = 256
	maxDescriptorBytes  = 4 * 1024 * 1024
	maxPolicyBytes      = 8 * 1024 * 1024
)

type Candidate struct {
	Revision    int64
	InputDigest string
	Releases    []Release
	Revocations []model.UpdaterRevocation
}

type Release struct {
	Name, Version, DescriptorDigest string
	Descriptor                      []byte
	Schemas                         []Schema
}

type Schema struct {
	Digest            string
	ObservationSchema []byte
	BudgetKeys        []string
}

type Snapshot struct {
	Revision     int64
	InputDigest  string
	PolicyDigest string
	Releases     []model.UpdaterRelease
	Tombstones   []model.UpdaterRevocation
	Schemas      []model.UpdaterSchemaEvidence
}

type ActivationResult struct {
	Changed  bool
	Snapshot Snapshot
}

type Store struct{ db *gorm.DB }

func NewStore(db *gorm.DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("updater catalog requires a database")
	}
	return &Store{db: db}, nil
}

func (s *Store) InitialMigration(ctx context.Context) error {
	db := store.DB(ctx, s.db)
	if err := db.AutoMigrate(&model.UpdaterCatalogHead{}, &model.UpdaterRelease{}, &model.UpdaterRevocation{}, &model.UpdaterSchemaEvidence{}, &model.UpdaterCatalogRefresh{}); err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.UpdaterCatalogHead{ID: 1}).Error
}

// Validate checks persistence metadata. Descriptor schemas and phase permissions
// must already have been validated by the administrator-config boundary.
func (c Candidate) Validate() error {
	if c.Revision <= 0 || !digestPattern.MatchString(c.InputDigest) || len(c.Releases)+len(c.Revocations) > 256 {
		return errors.New("invalid catalog revision, input digest or record count")
	}
	identities := map[string]bool{}
	for _, r := range c.Releases {
		key := r.Name + "/" + r.Version
		if !identityPattern.MatchString(r.Name) || !identityPattern.MatchString(r.Version) || identities[key] || !json.Valid(r.Descriptor) || len(r.Descriptor) > maxDescriptorBytes || hash(r.Descriptor) != r.DescriptorDigest || len(r.Schemas) == 0 || len(r.Schemas) > 2 {
			return errors.New("invalid release persistence metadata")
		}
		identities[key] = true
		for _, schema := range r.Schemas {
			if err := validateSchemaMetadata(schema); err != nil {
				return err
			}
		}
	}
	for _, r := range c.Revocations {
		key := r.Name + "/" + r.Version
		if !identityPattern.MatchString(r.Name) || !identityPattern.MatchString(r.Version) || identities[key] || !digestPattern.MatchString(r.DescriptorDigest) || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 1024 || r.Revision < 0 || r.Revision > c.Revision {
			return errors.New("invalid revocation persistence metadata")
		}
		identities[key] = true
	}
	return nil
}

func validateSchemaMetadata(s Schema) error {
	if !digestPattern.MatchString(s.Digest) || !json.Valid(s.ObservationSchema) || len(s.ObservationSchema) > 256*1024 || len(s.BudgetKeys) == 0 || len(s.BudgetKeys) > 64 {
		return errors.New("invalid retained schema metadata")
	}
	seen := map[string]bool{}
	for _, key := range s.BudgetKeys {
		if !identityPattern.MatchString(key) || seen[key] {
			return errors.New("invalid retained budget keys")
		}
		seen[key] = true
	}
	return nil
}

func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	var snapshot Snapshot
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		head, err := readHead(tx, "SHARE")
		if err != nil {
			return err
		}
		snapshot, err = readSnapshot(tx, head)
		return err
	})
	return snapshot, err
}

func readHead(tx *gorm.DB, strength string) (model.UpdaterCatalogHead, error) {
	var head model.UpdaterCatalogHead
	err := tx.Clauses(clause.Locking{Strength: strength}).First(&head, 1).Error
	return head, err
}

func readSnapshot(tx *gorm.DB, head model.UpdaterCatalogHead) (Snapshot, error) {
	result := Snapshot{Revision: head.Revision, InputDigest: head.InputDigest, PolicyDigest: head.PolicyDigest, Releases: []model.UpdaterRelease{}, Tombstones: []model.UpdaterRevocation{}, Schemas: []model.UpdaterSchemaEvidence{}}
	if err := tx.Find(&result.Releases).Error; err != nil {
		return Snapshot{}, err
	}
	if err := tx.Find(&result.Tombstones).Error; err != nil {
		return Snapshot{}, err
	}
	if err := tx.Find(&result.Schemas).Error; err != nil {
		return Snapshot{}, err
	}
	canonicalOrder(&result)
	return result, nil
}

func (s *Store) Activate(ctx context.Context, candidate Candidate) (ActivationResult, error) {
	if err := candidate.Validate(); err != nil {
		return ActivationResult{}, err
	}
	var result ActivationResult
	err := store.RunInTransaction(ctx, s.db, func(tx *gorm.DB) error {
		head, err := readHead(tx, "UPDATE")
		if err != nil {
			return err
		}
		if candidate.Revision < head.Revision {
			return ErrStaleRevision
		}
		if candidate.Revision == head.Revision && candidate.InputDigest != head.InputDigest {
			return ErrRevisionConflict
		}
		previous, err := readSnapshot(tx, head)
		if err != nil {
			return err
		}
		if candidate.Revision == head.Revision {
			result.Snapshot = previous
			return nil
		}
		next, err := mergeHistory(previous, candidate)
		if err != nil {
			return err
		}
		if err = persistDelta(tx, previous, next); err != nil {
			return err
		}
		if err = tx.Create(&model.UpdaterCatalogRefresh{Revision: candidate.Revision}).Error; err != nil {
			return err
		}
		write := tx.Model(&model.UpdaterCatalogHead{}).Where("id = ?", 1).Updates(map[string]any{"revision": candidate.Revision, "input_digest": candidate.InputDigest, "policy_digest": next.PolicyDigest})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return errors.New("updater catalog head disappeared")
		}
		result = ActivationResult{Changed: true, Snapshot: next}
		return nil
	})
	if err != nil {
		return ActivationResult{}, err
	}
	return result, nil
}

func mergeHistory(old Snapshot, c Candidate) (Snapshot, error) {
	next := old
	next.Revision = c.Revision
	next.InputDigest = c.InputDigest
	releases := map[string]model.UpdaterRelease{}
	tombstones := map[string]model.UpdaterRevocation{}
	schemas := map[string]model.UpdaterSchemaEvidence{}
	for _, r := range old.Releases {
		releases[r.Name+"/"+r.Version] = r
	}
	for _, r := range old.Tombstones {
		tombstones[r.Name+"/"+r.Version] = r
	}
	for _, s := range old.Schemas {
		schemas[s.Name+"@"+s.Digest] = s
	}
	incoming := map[string]bool{}
	for _, r := range c.Releases {
		key := r.Name + "/" + r.Version
		incoming[key] = true
		if _, revoked := tombstones[key]; revoked {
			return Snapshot{}, ErrHistoryConflict
		}
		if previous, ok := releases[key]; ok {
			if previous.DescriptorDigest != r.DescriptorDigest || !bytes.Equal(previous.Descriptor, r.Descriptor) {
				return Snapshot{}, ErrHistoryConflict
			}
		} else {
			releases[key] = model.UpdaterRelease{Name: r.Name, Version: r.Version, DescriptorDigest: r.DescriptorDigest, Descriptor: bytes.Clone(r.Descriptor)}
		}
		for _, s := range r.Schemas {
			keys := slices.Clone(s.BudgetKeys)
			slices.Sort(keys)
			evidence := model.UpdaterSchemaEvidence{Name: r.Name, Digest: s.Digest, ObservationSchema: bytes.Clone(s.ObservationSchema), BudgetKeys: keys}
			k := r.Name + "@" + s.Digest
			if previous, ok := schemas[k]; ok && (!bytes.Equal(previous.ObservationSchema, evidence.ObservationSchema) || !slices.Equal(previous.BudgetKeys, evidence.BudgetKeys)) {
				return Snapshot{}, ErrHistoryConflict
			}
			schemas[k] = evidence
		}
	}
	explicit := map[string]bool{}
	for _, r := range c.Revocations {
		key := r.Name + "/" + r.Version
		explicit[key] = true
		previous, ok := releases[key]
		if !ok || previous.DescriptorDigest != r.DescriptorDigest {
			return Snapshot{}, ErrHistoryConflict
		}
		if tombstone, exists := tombstones[key]; exists {
			if tombstone.DescriptorDigest != r.DescriptorDigest || tombstone.Reason != r.Reason || r.Revision != 0 && r.Revision != tombstone.Revision {
				return Snapshot{}, ErrHistoryConflict
			}
		} else {
			r.Revision = c.Revision
			tombstones[key] = r
		}
	}
	for _, r := range old.Tombstones {
		if !explicit[r.Name+"/"+r.Version] {
			return Snapshot{}, ErrHistoryConflict
		}
	}
	for key, r := range releases {
		if !incoming[key] {
			if _, ok := tombstones[key]; !ok {
				tombstones[key] = model.UpdaterRevocation{Name: r.Name, Version: r.Version, DescriptorDigest: r.DescriptorDigest, Reason: "omitted from administrator catalog", Revision: c.Revision}
			}
		}
	}
	next.Releases = mapValues(releases)
	next.Tombstones = mapValues(tombstones)
	next.Schemas = mapValues(schemas)
	canonicalOrder(&next)
	if err := boundPolicy(next); err != nil {
		return Snapshot{}, err
	}
	data, err := json.Marshal(struct {
		Revision   int64
		Releases   []model.UpdaterRelease
		Tombstones []model.UpdaterRevocation
	}{next.Revision, next.Releases, next.Tombstones})
	if err != nil {
		return Snapshot{}, err
	}
	next.PolicyDigest = hash(data)
	return next, nil
}

func mapValues[T any](values map[string]T) []T {
	result := make([]T, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func canonicalOrder(s *Snapshot) {
	slices.SortFunc(s.Releases, func(a, b model.UpdaterRelease) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Version, b.Version))
	})
	slices.SortFunc(s.Tombstones, func(a, b model.UpdaterRevocation) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Version, b.Version))
	})
	slices.SortFunc(s.Schemas, func(a, b model.UpdaterSchemaEvidence) int {
		return cmp.Or(cmp.Compare(a.Name, b.Name), cmp.Compare(a.Digest, b.Digest))
	})
}

func boundPolicy(s Snapshot) error {
	total := 0
	for _, r := range s.Releases {
		total += len(r.Descriptor)
	}
	if len(s.Releases) > maxRetainedReleases || total > maxDescriptorBytes {
		return errors.New("retained catalog exceeds descriptor budget")
	}
	// Reserve space for revoking every accepted release, including bounded reasons.
	// Revocation must remain possible even when descriptor capacity is exhausted.
	data, err := json.Marshal(struct {
		Releases   []model.UpdaterRelease
		Tombstones []model.UpdaterRevocation
	}{s.Releases, s.Tombstones})
	if err != nil {
		return err
	}
	if len(data) > maxPolicyBytes {
		return errors.New("retained catalog exceeds policy delivery budget")
	}
	return nil
}

func persistDelta(tx *gorm.DB, old, next Snapshot) error {
	oldReleases := map[string]bool{}
	for _, r := range old.Releases {
		oldReleases[r.Name+"/"+r.Version] = true
	}
	for _, r := range next.Releases {
		if !oldReleases[r.Name+"/"+r.Version] {
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
		}
	}
	oldTombstones := map[string]bool{}
	for _, r := range old.Tombstones {
		oldTombstones[r.Name+"/"+r.Version] = true
	}
	for _, r := range next.Tombstones {
		if !oldTombstones[r.Name+"/"+r.Version] {
			if err := tx.Create(&r).Error; err != nil {
				return err
			}
		}
	}
	oldSchemas := map[string]bool{}
	for _, s := range old.Schemas {
		oldSchemas[s.Name+"@"+s.Digest] = true
	}
	for _, s := range next.Schemas {
		if !oldSchemas[s.Name+"@"+s.Digest] {
			if err := tx.Create(&s).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) PendingRefresh(ctx context.Context, limit int) ([]model.UpdaterCatalogRefresh, error) {
	if limit <= 0 || limit > 256 {
		return nil, errors.New("refresh page limit must be between 1 and 256")
	}
	result := []model.UpdaterCatalogRefresh{}
	err := store.DB(ctx, s.db).Where("dispatched = ?", false).Order("revision").Limit(limit).Find(&result).Error
	return result, err
}
func hash(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
