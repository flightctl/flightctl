package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltagenerate "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

const testDeltaGenerationValidationInterval = time.Hour

type fakeValidationStore struct {
	generations []model.DeltaGeneration
	updateErr   error
}

func (f *fakeValidationStore) CreateDeltaGenerations(_ context.Context, _ []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return nil, nil
}

func (f *fakeValidationStore) InsertDeltaGenerations(_ context.Context, _ []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return nil, nil
}

func (f *fakeValidationStore) GetDeltaGeneration(_ context.Context, _ deltastore.GenerationKey, _ ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return nil, nil
}

func (f *fakeValidationStore) ListDeltaGenerations(_ context.Context, _ []deltastore.GenerationKey, _ ...deltastore.GenerationListOption) ([]model.DeltaGeneration, error) {
	return append([]model.DeltaGeneration(nil), f.generations...), nil
}

func (f *fakeValidationStore) UpdateDeltaGeneration(_ context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	if f.updateErr != nil {
		return nil, f.updateErr
	}
	for i := range f.generations {
		if f.generations[i].ResourceVersion != expectedResourceVersion {
			continue
		}
		f.generations[i] = *generation
		f.generations[i].ResourceVersion++
		return &f.generations[i], nil
	}
	return nil, nil
}

type fakeAvailabilityChecker struct {
	result artifactAvailability
	err    error
	calls  int
	check  func(model.DeltaGeneration) artifactAvailability
}

func (f *fakeAvailabilityChecker) Check(_ context.Context, generation model.DeltaGeneration) (artifactAvailability, error) {
	f.calls++
	if f.check != nil {
		return f.check(generation), nil
	}
	return f.result, f.err
}

type validationRepositoryService struct {
	repositoryservice.Service
	spec *domain.OciRepoSpec
	err  error
}

func (s *validationRepositoryService) ListRepositories(_ context.Context, _ uuid.UUID, _ domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status) {
	if s.err != nil {
		return nil, domain.StatusInternalServerError(s.err.Error())
	}
	if s.spec == nil {
		return &domain.RepositoryList{}, domain.StatusOK()
	}
	spec := domain.RepositorySpec{}
	if err := spec.FromOciRepoSpec(*s.spec); err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}
	return &domain.RepositoryList{Items: []domain.Repository{{Spec: spec}}}, domain.StatusOK()
}

type recordingDeltaHintKVStore struct {
	kvstore.KVStore
	deleted []string
	values  map[string][]byte
	errFor  map[string]error
}

func (s *recordingDeltaHintKVStore) Get(_ context.Context, key string) ([]byte, error) {
	return append([]byte(nil), s.values[key]...), nil
}

func (s *recordingDeltaHintKVStore) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[key] = append([]byte(nil), value...)
	return nil
}

func (s *recordingDeltaHintKVStore) Delete(_ context.Context, key string) error {
	if key == deltaGenerationValidationCursorKey {
		delete(s.values, key)
	} else {
		s.deleted = append(s.deleted, key)
	}
	if s.errFor != nil {
		if err, ok := s.errFor[key]; ok {
			return err
		}
	}
	return nil
}

func newDeltaValidationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", uuid.New())), &gorm.Config{})
	require.NoError(t, err)
	return db
}

func newTestDeltaGenerationValidator(t *testing.T, log logrus.FieldLogger, generations deltastore.Store, checker artifactAvailabilityChecker) (*DeltaGenerationValidator, *recordingDeltaHintKVStore) {
	t.Helper()
	kv := &recordingDeltaHintKVStore{}
	validator, err := newDeltaGenerationValidator(log, generations, kv, checker, testDeltaGenerationValidationInterval)
	require.NoError(t, err)
	return validator, kv
}

func newTestOCIDeltaArtifactChecker(t *testing.T, spec *domain.OciRepoSpec, resolveErr error) *ociDeltaArtifactChecker {
	t.Helper()
	checker, err := newOCIDeltaArtifactChecker(time.Second, &validationRepositoryService{spec: spec, err: resolveErr}, nil)
	require.NoError(t, err)
	return checker
}

func assertEvictedDeltaHints(t *testing.T, kv *recordingDeltaHintKVStore, key deltastore.GenerationKey) {
	t.Helper()
	require.Len(t, kv.deleted, 2)
	assert.Contains(t, kv.deleted, deltaGenerationHintKey(key))
	assert.Contains(t, kv.deleted, deltaGenerationLookupMemoKey(key))
}

func TestDeltaGenerationValidatorPoll(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	t.Run("When a stored delta artifact is missing it should fail the generation and evict hints", func(t *testing.T) {
		orgID := uuid.New()
		generation := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			SizeBytes:       lo.ToPtr(int64(1234)),
			ResourceVersion: 7,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		checker := &fakeAvailabilityChecker{result: artifactMissing}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Nil(t, store.generations[0].SizeBytes)
		assert.Equal(t, int64(8), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When a stored delta artifact is available it should record verification without invalidating it", func(t *testing.T) {
		orgID := uuid.New()
		generation := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			SizeBytes:       lo.ToPtr(int64(1234)),
			ResourceVersion: 7,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		checker := &fakeAvailabilityChecker{result: artifactAvailable}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationSucceeded, store.generations[0].Status)
		assert.Equal(t, *generation.DeltaRef, *store.generations[0].DeltaRef)
		assert.Equal(t, int64(1234), *store.generations[0].SizeBytes)
		assert.Equal(t, int64(8), store.generations[0].ResourceVersion)
		assert.NotNil(t, store.generations[0].LastVerifiedAt)
		assert.Empty(t, hints.deleted)
	})

	t.Run("When a succeeded generation has no artifact reference it should fail without checking the registry", func(t *testing.T) {
		orgID := uuid.New()
		generation := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			ResourceVersion: 7,
		}
		db := newDeltaValidationTestDB(t)
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(context.Background()))
		require.NoError(t, db.Create(&generation).Error)
		checker := &fakeAvailabilityChecker{result: artifactAvailable}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(context.Background())

		var updated model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ?", orgID).First(&updated).Error)
		assert.Equal(t, model.DeltaGenerationFailed, updated.Status)
		assert.Nil(t, updated.DeltaRef)
		assert.Equal(t, int64(8), updated.ResourceVersion)
		assert.Zero(t, checker.calls)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When a generation was verified within the interval it should remain unchanged", func(t *testing.T) {
		now := time.Now()
		stale := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/stale",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/stale@sha256:delta"),
			LastVerifiedAt:  lo.ToPtr(now.Add(-2 * testDeltaGenerationValidationInterval)),
			ResourceVersion: 1,
		}
		recent := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/recent",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/recent@sha256:delta"),
			LastVerifiedAt:  lo.ToPtr(now.Add(-testDeltaGenerationValidationInterval / 2)),
			ResourceVersion: 1,
		}
		db := newDeltaValidationTestDB(t)
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(context.Background()))
		require.NoError(t, db.Create(&stale).Error)
		require.NoError(t, db.Create(&recent).Error)

		checker := &fakeAvailabilityChecker{result: artifactMissing}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(context.Background())

		var updatedStale model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", stale.OrgID, stale.ImageRepository).First(&updatedStale).Error)
		assert.Equal(t, model.DeltaGenerationFailed, updatedStale.Status)
		assert.Equal(t, int64(2), updatedStale.ResourceVersion)

		var unchangedRecent model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", recent.OrgID, recent.ImageRepository).First(&unchangedRecent).Error)
		assert.Equal(t, model.DeltaGenerationSucceeded, unchangedRecent.Status)
		assert.Equal(t, int64(1), unchangedRecent.ResourceVersion)
		assert.Equal(t, 1, checker.calls)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(stale))
	})

	t.Run("When a stored artifact reference is malformed it should invalidate the generation", func(t *testing.T) {
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@latest"),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, nil, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When the stored artifact registry returns not found it should invalidate the generation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{Registry: host, Scheme: &scheme}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + digest.FromString("delta").String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When a full batch is processed it should persist a cursor and continue on the next poll", func(t *testing.T) {
		db := newDeltaValidationTestDB(t)
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(context.Background()))

		orgID := uuid.New()
		generations := make([]model.DeltaGeneration, 501)
		for i := range generations {
			generations[i] = model.DeltaGeneration{
				OrgID:           orgID,
				ImageRepository: fmt.Sprintf("quay.io/team/os/%03d", i),
				SourceDigest:    "sha256:source",
				TargetDigest:    "sha256:target",
				Status:          model.DeltaGenerationSucceeded,
				DeltaRef:        lo.ToPtr(fmt.Sprintf("quay.io/team/os/%03d@sha256:delta", i)),
				ResourceVersion: 1,
			}
			require.NoError(t, db.Create(&generations[i]).Error)
		}

		checker := &fakeAvailabilityChecker{
			check: func(generation model.DeltaGeneration) artifactAvailability {
				if generation.ImageRepository == "quay.io/team/os/500" {
					return artifactMissing
				}
				return artifactUnknown
			},
		}
		validator, kv := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(context.Background())

		var updated model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, "quay.io/team/os/500").First(&updated).Error)
		assert.Equal(t, model.DeltaGenerationSucceeded, updated.Status)
		assert.Equal(t, 500, checker.calls)

		var cursor deltaGenerationValidationCursor
		require.NoError(t, json.Unmarshal(kv.values[deltaGenerationValidationCursorKey], &cursor))
		require.NotNil(t, cursor.After)
		assert.Equal(t, deltaGenerationKey(generations[499]), *cursor.After)

		validator.Poll(context.Background())

		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, "quay.io/team/os/500").First(&updated).Error)
		assert.Equal(t, model.DeltaGenerationFailed, updated.Status)
		assert.Nil(t, updated.DeltaRef)
		assert.Equal(t, int64(2), updated.ResourceVersion)
		assert.Equal(t, 501, checker.calls)
		assert.NotContains(t, kv.values, deltaGenerationValidationCursorKey)
		assertEvictedDeltaHints(t, kv, deltaGenerationKey(generations[500]))
	})

	t.Run("When an authenticated registry denies access it should invalidate the generation", func(t *testing.T) {
		authenticated := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username != "delta-user" || password != "delta-pass" {
				w.Header().Set("Www-Authenticate", `Basic realm="Delta Registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authenticated = true
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{
			Registry: host,
			Scheme:   &scheme,
			OciAuth:  makeDockerOciAuth("delta-user", "delta-pass"),
		}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + digest.FromString("delta").String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.True(t, authenticated)
		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When the configured registry host differs only in case it should authenticate the exact artifact reference", func(t *testing.T) {
		authenticated := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username != "delta-user" || password != "delta-pass" {
				w.Header().Set("Www-Authenticate", `Basic realm="Delta Registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authenticated = true
			w.WriteHeader(http.StatusForbidden)
		}))
		t.Cleanup(server.Close)
		_, port, found := strings.Cut(server.Listener.Addr().String(), ":")
		require.True(t, found)
		artifactHost := "localhost:" + port
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{
			Registry: strings.ToUpper(artifactHost),
			Scheme:   &scheme,
			OciAuth:  makeDockerOciAuth("delta-user", "delta-pass"),
		}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: artifactHost + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(artifactHost + "/team/os@" + digest.FromString("delta").String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.True(t, authenticated)
		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When the invocation time budget expires it should resume with later generations", func(t *testing.T) {
		db := newDeltaValidationTestDB(t)
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(context.Background()))

		orgID := uuid.New()
		generations := make([]model.DeltaGeneration, 3)
		for index := range generations {
			generations[index] = model.DeltaGeneration{
				OrgID:           orgID,
				ImageRepository: fmt.Sprintf("quay.io/team/os/%d", index),
				SourceDigest:    "sha256:source",
				TargetDigest:    "sha256:target",
				Status:          model.DeltaGenerationSucceeded,
				DeltaRef:        lo.ToPtr(fmt.Sprintf("quay.io/team/os/%d@sha256:delta", index)),
				ResourceVersion: 1,
			}
			require.NoError(t, db.Create(&generations[index]).Error)
		}

		checker := &fakeAvailabilityChecker{result: artifactMissing}
		validator, _ := newTestDeltaGenerationValidator(t, log, store, checker)
		validator.timeBudget = -time.Second

		validator.Poll(context.Background())

		var updated model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, generations[0].ImageRepository).First(&updated).Error)
		assert.Equal(t, model.DeltaGenerationFailed, updated.Status)
		for _, generation := range generations[1:] {
			var unchanged model.DeltaGeneration
			require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, generation.ImageRepository).First(&unchanged).Error)
			assert.Equal(t, model.DeltaGenerationSucceeded, unchanged.Status)
		}
		assert.Equal(t, 1, checker.calls)

		validator.timeBudget = deltaGenerationValidationTimeBudget
		validator.Poll(context.Background())

		for _, generation := range generations {
			var updated model.DeltaGeneration
			require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, generation.ImageRepository).First(&updated).Error)
			assert.Equal(t, model.DeltaGenerationFailed, updated.Status)
		}
		assert.Equal(t, 3, checker.calls)
	})

	t.Run("When artifact checking fails transiently it should retain the generation", func(t *testing.T) {
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@" + digest.FromString("delta").String()),
			ResourceVersion: 7,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, &fakeAvailabilityChecker{err: assert.AnError})

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationSucceeded, store.generations[0].Status)
		assert.Equal(t, *generation.DeltaRef, *store.generations[0].DeltaRef)
		assert.Equal(t, int64(7), store.generations[0].ResourceVersion)
		assert.Empty(t, hints.deleted)
	})

	t.Run("When an authenticated registry denies referrer listing it should invalidate the generation", func(t *testing.T) {
		targetDigest := digest.FromString("target")
		manifest := ocispec.Manifest{
			ArtifactType: deltagenerate.OCIDeltaArtifactType,
			Config:       ocispec.Descriptor{Digest: digest.FromString("config"), Size: 10},
			Layers: []ocispec.Descriptor{
				{Digest: digest.FromString("layer"), Size: 20},
			},
			Subject:     &ocispec.Descriptor{Digest: targetDigest},
			Annotations: map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
		}
		manifestBytes, err := json.Marshal(manifest)
		require.NoError(t, err)
		deltaDigest := digest.FromBytes(manifestBytes)
		referrersDenied := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username != "delta-user" || password != "delta-pass" {
				w.Header().Set("Www-Authenticate", `Basic realm="Delta Registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.Contains(r.URL.Path, "/referrers/") {
				referrersDenied = true
				w.Header().Set("Www-Authenticate", `Basic realm="Delta Registry"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", deltaDigest.String())
			_, _ = w.Write(manifestBytes)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{
			Registry: host,
			Scheme:   &scheme,
			OciAuth:  makeDockerOciAuth("delta-user", "delta-pass"),
		}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    targetDigest.String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + deltaDigest.String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.True(t, referrersDenied)
		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When repository credentials cannot be resolved it should retain the generation", func(t *testing.T) {
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@" + digest.FromString("delta").String()),
			ResourceVersion: 7,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, nil, assert.AnError))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationSucceeded, store.generations[0].Status)
		assert.Equal(t, int64(7), store.generations[0].ResourceVersion)
		assert.Empty(t, hints.deleted)
	})

	t.Run("When the stored artifact manifest targets another image it should invalidate the generation", func(t *testing.T) {
		manifest := ocispec.Manifest{
			ArtifactType: deltagenerate.OCIDeltaArtifactType,
			Config:       ocispec.Descriptor{Digest: digest.FromString("config"), Size: 10},
			Layers: []ocispec.Descriptor{
				{Digest: digest.FromString("layer"), Size: 20},
			},
			Subject:     &ocispec.Descriptor{Digest: digest.FromString("other-target")},
			Annotations: map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
		}
		manifestBytes, err := json.Marshal(manifest)
		require.NoError(t, err)
		deltaDigest := digest.FromBytes(manifestBytes)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", deltaDigest.String())
			_, _ = w.Write(manifestBytes)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{Registry: host, Scheme: &scheme}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + deltaDigest.String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When the current configured delta target changes it should retain an available artifact", func(t *testing.T) {
		targetDigest := digest.FromString("target")
		manifest := ocispec.Manifest{
			ArtifactType: deltagenerate.OCIDeltaArtifactType,
			Config:       ocispec.Descriptor{Digest: digest.FromString("config"), Size: 10},
			Layers: []ocispec.Descriptor{
				{Digest: digest.FromString("layer"), Size: 20},
			},
			Subject:     &ocispec.Descriptor{Digest: targetDigest},
			Annotations: map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
		}
		manifestBytes, err := json.Marshal(manifest)
		require.NoError(t, err)
		deltaDigest := digest.FromBytes(manifestBytes)
		referrersBytes, err := json.Marshal(ocispec.Index{
			Manifests: []ocispec.Descriptor{
				{
					MediaType:    ocispec.MediaTypeImageManifest,
					Digest:       digest.FromString("other-delta"),
					Size:         10,
					ArtifactType: deltagenerate.OCIDeltaArtifactType,
					Annotations:  map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
				},
				{
					MediaType:    ocispec.MediaTypeImageManifest,
					Digest:       deltaDigest,
					Size:         int64(len(manifestBytes)),
					ArtifactType: deltagenerate.OCIDeltaArtifactType,
					Annotations:  map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
				},
			},
		})
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/referrers/") {
				w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
				_, _ = w.Write(referrersBytes)
				return
			}
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", deltaDigest.String())
			_, _ = w.Write(manifestBytes)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{
			Registry:   host,
			Repository: lo.ToPtr("new-team/deltas"),
			Scheme:     &scheme,
		}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    targetDigest.String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + deltaDigest.String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationSucceeded, store.generations[0].Status)
		assert.Equal(t, *generation.DeltaRef, *store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assert.NotNil(t, store.generations[0].LastVerifiedAt)
		assert.Empty(t, hints.deleted)
	})

	t.Run("When a compare-and-set update misses it should retain the generation and hints", func(t *testing.T) {
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			ResourceVersion: 7,
		}
		store := &fakeValidationStore{
			generations: []model.DeltaGeneration{generation},
			updateErr:   flterrors.ErrNoRowsUpdated,
		}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, &fakeAvailabilityChecker{result: artifactMissing})

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationSucceeded, store.generations[0].Status)
		assert.Equal(t, "quay.io/team/os@sha256:delta", *store.generations[0].DeltaRef)
		assert.Equal(t, int64(7), store.generations[0].ResourceVersion)
		assert.Empty(t, hints.deleted)
	})

	t.Run("When the stored artifact manifest is invalid it should invalidate the generation", func(t *testing.T) {
		manifest := []byte(`{}`)
		deltaDigest := digest.FromBytes(manifest)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", deltaDigest.String())
			_, _ = w.Write(manifest)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{Registry: host, Scheme: &scheme}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    digest.FromString("target").String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + deltaDigest.String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When the target has no exact matching delta referrer it should invalidate the generation", func(t *testing.T) {
		targetDigest := digest.FromString("target")
		manifest := ocispec.Manifest{
			ArtifactType: deltagenerate.OCIDeltaArtifactType,
			Config:       ocispec.Descriptor{Digest: digest.FromString("config"), Size: 10},
			Layers: []ocispec.Descriptor{
				{Digest: digest.FromString("layer"), Size: 20},
			},
			Subject:     &ocispec.Descriptor{Digest: targetDigest},
			Annotations: map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
		}
		manifestBytes, err := json.Marshal(manifest)
		require.NoError(t, err)
		deltaDigest := digest.FromBytes(manifestBytes)
		referrersBytes, err := json.Marshal(ocispec.Index{
			Manifests: []ocispec.Descriptor{{
				MediaType:    ocispec.MediaTypeImageManifest,
				Digest:       digest.FromString("other-delta"),
				Size:         10,
				ArtifactType: deltagenerate.OCIDeltaArtifactType,
				Annotations:  map[string]string{deltagenerate.OCIDeltaSourceAnnotation: digest.FromString("source").String()},
			}},
		})
		require.NoError(t, err)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.Contains(r.URL.Path, "/referrers/") {
				w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
				_, _ = w.Write(referrersBytes)
				return
			}
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", deltaDigest.String())
			_, _ = w.Write(manifestBytes)
		}))
		t.Cleanup(server.Close)
		host := server.Listener.Addr().String()
		scheme := domain.OciRepoSchemeHttp
		spec := &domain.OciRepoSpec{Registry: host, Scheme: &scheme}
		generation := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: host + "/team/os",
			SourceDigest:    digest.FromString("source").String(),
			TargetDigest:    targetDigest.String(),
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr(host + "/team/os@" + deltaDigest.String()),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, newTestOCIDeltaArtifactChecker(t, spec, nil))

		validator.Poll(context.Background())

		require.Len(t, store.generations, 1)
		assert.Equal(t, model.DeltaGenerationFailed, store.generations[0].Status)
		assert.Nil(t, store.generations[0].DeltaRef)
		assert.Equal(t, int64(2), store.generations[0].ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(generation))
	})

	t.Run("When polling with the generation service it should list and validate only succeeded generations", func(t *testing.T) {
		db := newDeltaValidationTestDB(t)
		ctx := context.Background()
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(ctx))

		orgID := uuid.New()
		succeeded := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			SizeBytes:       lo.ToPtr(int64(1234)),
			ResourceVersion: 1,
		}
		pending := model.DeltaGeneration{
			OrgID:           uuid.New(),
			ImageRepository: "quay.io/team/pending",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationPending,
			ResourceVersion: 1,
		}
		require.NoError(t, db.Create(&succeeded).Error)
		require.NoError(t, db.Create(&pending).Error)

		checker := &fakeAvailabilityChecker{result: artifactMissing}
		validator, hints := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(ctx)

		var updated model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", orgID, "quay.io/team/os").First(&updated).Error)
		assert.Equal(t, model.DeltaGenerationFailed, updated.Status)
		assert.Nil(t, updated.DeltaRef)
		assert.Nil(t, updated.SizeBytes)
		assert.Equal(t, int64(2), updated.ResourceVersion)

		var unchanged model.DeltaGeneration
		require.NoError(t, db.Where("org_id = ? AND image_repository = ?", pending.OrgID, "quay.io/team/pending").First(&unchanged).Error)
		assert.Equal(t, model.DeltaGenerationPending, unchanged.Status)
		assert.Equal(t, int64(1), unchanged.ResourceVersion)
		assertEvictedDeltaHints(t, hints, deltaGenerationKey(succeeded))
	})

	t.Run("When polling with the KV hint cache it should evict both success hints", func(t *testing.T) {
		db := newDeltaValidationTestDB(t)
		ctx := context.Background()
		store := deltastore.NewStore(db, log)
		require.NoError(t, store.InitialMigration(ctx))

		orgID := uuid.New()
		generation := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			ResourceVersion: 1,
		}
		require.NoError(t, db.Create(&generation).Error)

		checker := &fakeAvailabilityChecker{result: artifactMissing}
		validator, kv := newTestDeltaGenerationValidator(t, log, store, checker)

		validator.Poll(ctx)

		assertEvictedDeltaHints(t, kv, deltaGenerationKey(generation))
	})

	t.Run("When one hint cache eviction fails it should still attempt the other hint", func(t *testing.T) {
		orgID := uuid.New()
		generation := model.DeltaGeneration{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
			Status:          model.DeltaGenerationSucceeded,
			DeltaRef:        lo.ToPtr("quay.io/team/os@sha256:delta"),
			ResourceVersion: 1,
		}
		store := &fakeValidationStore{generations: []model.DeltaGeneration{generation}}
		key := deltastore.GenerationKey{
			OrgID:           orgID,
			ImageRepository: "quay.io/team/os",
			SourceDigest:    "sha256:source",
			TargetDigest:    "sha256:target",
		}
		kv := &recordingDeltaHintKVStore{
			errFor: map[string]error{deltaGenerationHintKey(key): assert.AnError},
		}
		validator, err := newDeltaGenerationValidator(log, store, kv, &fakeAvailabilityChecker{result: artifactMissing}, testDeltaGenerationValidationInterval)
		require.NoError(t, err)

		validator.Poll(context.Background())

		assertEvictedDeltaHints(t, kv, key)
	})
}
