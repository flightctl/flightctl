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
	kind string
	rows []EncryptionMigratableRow
}

func (r *fakeResource) Kind() string { return r.kind }

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
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func newTestMigrator(t *testing.T, mgr *encryption.Manager, checkpoints CheckpointStore) *EncryptionMigrator {
	t.Helper()
	db := openTestDB(t)
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	return NewEncryptionMigrator(context.Background(), db, mgr, checkpoints, nil, nil, log)
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

	raw, err := checkpoints.Get(context.Background(), EncryptionMigrationConsumer, checkpointKey("Fake", org))
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

	raw, err := checkpoints.Get(context.Background(), EncryptionMigrationConsumer, checkpointKey("Fake", org))
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
	require.NoError(t, checkpoints.Set(context.Background(), EncryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID: "old-key",
		RegistryHash:      model.RegistryHash(),
		Complete:          true,
		LastName:          "a",
	})))

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.False(t, report.Complete)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, "default", report.ActiveKeyID)
}

func TestEncryptionMigrator_WhenRegistryHashChangesItShouldReset(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	migrator := newTestMigrator(t, mgr, checkpoints)
	org := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{org})
	migrator.SetRegistryHashOverride("hash-v1")
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: []EncryptionMigratableRow{
			&fakeRow{orgID: org, name: "a", changed: true, keyIDs: []string{"default"}},
		}},
	}
	require.NoError(t, checkpoints.Set(context.Background(), EncryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID: "default",
		RegistryHash:      "hash-v1",
		Complete:          true,
		LastName:          "a",
	})))

	migrator.SetRegistryHashOverride("hash-v2")
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
		created_at DATETIME,
		updated_at DATETIME,
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
	require.NoError(t, db.Table("repositories").Create(map[string]any{
		"org_id":           org,
		"name":             "repo-1",
		"resource_version": 1,
		"spec":             mustJSON(t, spec),
	}).Error)

	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	migrator := NewEncryptionMigrator(context.Background(), db, mgr, newMemoryCheckpointStore(), nil, nil, log)
	migrator.SetBatchSize(10)

	report, err := migrator.RunBatch(context.Background(), domain.RepositoryKind, org)
	require.NoError(t, err)
	assert.Equal(t, 1, report.Scanned)
	assert.Equal(t, 1, report.Updated)
	assert.Contains(t, report.KeyIDsInUse, "default")

	var stored model.Repository
	require.NoError(t, db.First(&stored, "org_id = ? AND name = ?", org, "repo-1").Error)
	storedJSON, err := json.Marshal(stored.Spec.Data)
	require.NoError(t, err)
	assert.NotContains(t, string(storedJSON), "super-secret")
	assert.Contains(t, string(storedJSON), "enc:v1:default:")

	report, err = migrator.RunBatch(context.Background(), domain.RepositoryKind, org)
	require.NoError(t, err)
	assert.True(t, report.Complete)
}

func TestPersistMigratedModel_WhenResourceVersionConflictsItShouldError(t *testing.T) {
	db := openTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE repositories (
		org_id TEXT NOT NULL,
		name TEXT NOT NULL,
		resource_version INTEGER,
		created_at DATETIME,
		updated_at DATETIME,
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
	newVersion := staleVersion + 1
	row := &model.Repository{
		Resource: model.Resource{OrgID: org, Name: "repo-1", ResourceVersion: &newVersion},
		Spec:     model.MakeJSONField(spec),
	}
	err := persistMigratedModel(context.Background(), db, row, org, "repo-1", staleVersion, []string{"Spec", "ResourceVersion"})
	require.Error(t, err)
	assert.ErrorIs(t, err, flterrors.ErrResourceVersionConflict)
}

func TestAuthProviderEncryptionMigration_EndToEnd(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	db := openTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE auth_providers (
		org_id TEXT NOT NULL,
		name TEXT NOT NULL,
		resource_version INTEGER,
		created_at DATETIME,
		updated_at DATETIME,
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
	migrator := NewEncryptionMigrator(context.Background(), db, mgr, newMemoryCheckpointStore(), nil, nil, log)

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
	migrator.SetRegistryHashOverride("test-hash")
	require.NoError(t, checkpoints.Set(context.Background(), EncryptionMigrationConsumer, checkpointKey("Fake", org), mustJSON(t, EncryptionMigrationCheckpoint{
		TargetActiveKeyID: "default",
		RegistryHash:      "test-hash",
		Complete:          true,
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

func TestRunEncryptionMigrationBatch_WhenLeaseBusyItShouldReEnqueueAfterDelay(t *testing.T) {
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
	assert.Empty(t, publisher.workItems(), "busy lease should not enqueue immediately")
	require.Eventually(t, func() bool {
		return len(publisher.workItems()) == 1
	}, 2*time.Second, 20*time.Millisecond)
	assert.Equal(t, []EncryptionMigrationWork{{Kind: "Fake", OrgID: org}}, publisher.workItems())
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
	k1Repo, k2Repo := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgA))
	k1AP, k2AP := encryptionMigrationAdvisoryKeys(leaseKey(domain.AuthProviderKind, orgA))
	assert.False(t, k1Repo == k1AP && k2Repo == k2AP)

	k1OrgA, k2OrgA := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgA))
	k1OrgB, k2OrgB := encryptionMigrationAdvisoryKeys(leaseKey(domain.RepositoryKind, orgB))
	assert.False(t, k1OrgA == k1OrgB && k2OrgA == k2OrgB)
}

func TestEncryptionMigrator_WhenHandlersChangeItShouldHaveMatchingResources(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	migrator := newTestMigrator(t, mgr, newMemoryCheckpointStore())

	handlerKinds := model.EncryptionHandlers()
	for kind := range handlerKinds {
		_, ok := migrator.resources[kind]
		assert.True(t, ok, "EncryptionHandlers has kind %q but migrator has no resource adapter", kind)
	}
	for kind := range migrator.resources {
		_, ok := handlerKinds[kind]
		assert.True(t, ok, "migrator has resource adapter for kind %q but EncryptionHandlers does not", kind)
	}
}

type recordingCanaryService struct {
	mu       sync.Mutex
	canaries []encryption.Canary
	retired  []string
}

func (s *recordingCanaryService) Get(context.Context, string, string) (*encryption.Canary, domain.Status) {
	return nil, domain.StatusOK()
}
func (s *recordingCanaryService) Save(context.Context, *encryption.Canary) domain.Status {
	return domain.StatusOK()
}
func (s *recordingCanaryService) GetAll(context.Context) ([]encryption.Canary, domain.Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]encryption.Canary, len(s.canaries))
	copy(out, s.canaries)
	return out, domain.StatusOK()
}
func (s *recordingCanaryService) PrepareForRetirement(_ context.Context, strategy, keyID string) domain.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retired = append(s.retired, strategy+"/"+keyID)
	return domain.StatusOK()
}

func TestEncryptionMigrator_WhenMigrationCompleteItShouldPrepareKeyRetirement(t *testing.T) {
	mgr := newLocalEncryptionManager(t)
	checkpoints := newMemoryCheckpointStore()
	canaries := &recordingCanaryService{
		canaries: []encryption.Canary{
			{Strategy: "v1", KeyID: "default"},
			{Strategy: "v1", KeyID: "old-key"},
		},
	}
	db := openTestDB(t)
	log := logrus.New()
	log.SetLevel(logrus.FatalLevel)
	migrator := NewEncryptionMigrator(context.Background(), db, mgr, checkpoints, nil, canaries, log)
	org := uuid.New()
	migrator.SetOrganizations([]uuid.UUID{org})
	migrator.SetRegistryHashOverride("hash")
	migrator.resources = map[string]EncryptionMigrationResource{
		"Fake": &fakeResource{kind: "Fake", rows: nil},
	}

	report, err := migrator.RunBatch(context.Background(), "Fake", org)
	require.NoError(t, err)
	assert.True(t, report.Complete)

	canaries.mu.Lock()
	defer canaries.mu.Unlock()
	assert.Equal(t, []string{"v1/old-key"}, canaries.retired)
}
