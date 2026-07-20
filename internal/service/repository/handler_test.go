package repository

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

const (
	statusSuccessCode    = int32(200)
	statusCreatedCode    = int32(201)
	statusBadRequestCode = int32(400)
	statusNotFoundCode   = int32(404)
)

// fakeRepositoryStore is a small in-memory implementation of internal/store/repository.Store.
type fakeRepositoryStore struct {
	items      map[string]*domain.Repository
	fleetRefs  map[string]*domain.FleetList
	deviceRefs map[string]*domain.DeviceList
}

func newFakeRepositoryStore() *fakeRepositoryStore {
	return &fakeRepositoryStore{
		items:      map[string]*domain.Repository{},
		fleetRefs:  map[string]*domain.FleetList{},
		deviceRefs: map[string]*domain.DeviceList{},
	}
}

func (f *fakeRepositoryStore) InitialMigration(_ context.Context) error { return nil }

func (f *fakeRepositoryStore) Create(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	if _, exists := f.items[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.items[name] = repository
	if eventCallback != nil {
		eventCallback(ctx, domain.RepositoryKind, orgId, name, nil, repository, true, nil)
	}
	return repository, nil
}

func (f *fakeRepositoryStore) Update(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	old, exists := f.items[name]
	if !exists {
		return nil, flterrors.ErrResourceNotFound
	}
	f.items[name] = repository
	if eventCallback != nil {
		eventCallback(ctx, domain.RepositoryKind, orgId, name, old, repository, false, nil)
	}
	return repository, nil
}

func (f *fakeRepositoryStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, bool, error) {
	name := lo.FromPtr(repository.Metadata.Name)
	if _, exists := f.items[name]; exists {
		result, err := f.Update(ctx, orgId, repository, eventCallback)
		return result, false, err
	}
	result, err := f.Create(ctx, orgId, repository, eventCallback)
	return result, true, err
}

func (f *fakeRepositoryStore) Get(_ context.Context, _ uuid.UUID, name string) (*domain.Repository, error) {
	r, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return r, nil
}

func (f *fakeRepositoryStore) List(_ context.Context, _ uuid.UUID, _ store.ListParams) (*domain.RepositoryList, error) {
	var items []domain.Repository
	for _, r := range f.items {
		items = append(items, *r)
	}
	return &domain.RepositoryList{Items: items}, nil
}

func (f *fakeRepositoryStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	if _, exists := f.items[name]; !exists {
		return nil
	}
	delete(f.items, name)
	if eventCallback != nil {
		eventCallback(ctx, domain.RepositoryKind, orgId, name, nil, nil, false, nil)
	}
	return nil
}

func (f *fakeRepositoryStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Repository, eventCallback store.EventCallback) (*domain.Repository, error) {
	name := lo.FromPtr(resource.Metadata.Name)
	existing, ok := f.items[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	old := *existing
	existing.Status = resource.Status
	if eventCallback != nil {
		eventCallback(ctx, domain.RepositoryKind, orgId, name, &old, existing, false, nil)
	}
	return existing, nil
}

func (f *fakeRepositoryStore) GetFleetRefs(_ context.Context, _ uuid.UUID, name string) (*domain.FleetList, error) {
	if refs, ok := f.fleetRefs[name]; ok {
		return refs, nil
	}
	return &domain.FleetList{}, nil
}

func (f *fakeRepositoryStore) GetDeviceRefs(_ context.Context, _ uuid.UUID, name string) (*domain.DeviceList, error) {
	if refs, ok := f.deviceRefs[name]; ok {
		return refs, nil
	}
	return &domain.DeviceList{}, nil
}

func (f *fakeRepositoryStore) Count(_ context.Context, _ uuid.UUID, _ store.ListParams) (int64, error) {
	return int64(len(f.items)), nil
}

func (f *fakeRepositoryStore) CountByOrg(_ context.Context, _ *uuid.UUID) ([]store.CountByOrgResult, error) {
	return nil, nil
}

// fakeEventsService is a recording fake for events.Service. Repository's own event decision
// logic (in handler.go's callbackRepositoryUpdated) now calls CreateEvent directly, so tests
// assert on the actual emitted events rather than intercepting a resource-specific callback.
type fakeEventsService struct {
	events.Service
	created []*domain.Event
	deleted []recordedCallback
}

type recordedCallback struct {
	orgId   uuid.UUID
	name    string
	created bool
	err     error
}

func (f *fakeEventsService) CreateEvent(_ context.Context, _ uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	f.created = append(f.created, event)
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(_ context.Context, _ domain.ResourceKind, orgId uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	f.deleted = append(f.deleted, recordedCallback{orgId: orgId, name: name, created: created, err: err})
}

func newTestHandler() (*ServiceHandler, *fakeRepositoryStore, *fakeEventsService) {
	repoStore := newFakeRepositoryStore()
	evStore := &fakeEventsService{}
	return NewServiceHandler(repoStore, evStore, logrus.New()), repoStore, evStore
}

func newGitRepository(name, url string) domain.Repository {
	spec := domain.RepositorySpec{}
	_ = spec.FromGitRepoSpec(domain.GitRepoSpec{Url: url, Type: domain.GitRepoSpecTypeGit})
	return domain.Repository{
		ApiVersion: "v1beta1",
		Kind:       "Repository",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec:       spec,
	}
}

func newOciRepository(name, registry string) domain.Repository {
	spec := domain.RepositorySpec{}
	_ = spec.FromOciRepoSpec(domain.OciRepoSpec{Registry: registry, Type: domain.OciRepoSpecTypeOci, Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)})
	return domain.Repository{
		ApiVersion: "v1beta1",
		Kind:       "Repository",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name)},
		Spec:       spec,
	}
}

// ── CreateRepository ─────────────────────────────────────────────────────

func TestCreateRepository(t *testing.T) {
	t.Run("When the repository is valid it should create it and fire an updated callback", func(t *testing.T) {
		h, repoStore, ev := newTestHandler()
		resp, status := h.CreateRepository(context.Background(), uuid.New(), newGitRepository("git-repo", "https://example.com/repo.git"))
		require.Equal(t, statusCreatedCode, status.Code)
		require.NotNil(t, resp)
		require.Equal(t, "git-repo", *resp.Metadata.Name)
		_, err := repoStore.Get(context.Background(), uuid.Nil, "git-repo")
		require.NoError(t, err)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When the repository spec is invalid it should return 400", func(t *testing.T) {
		h, _, _ := newTestHandler()
		invalid := domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("bad-repo")}}
		_, status := h.CreateRepository(context.Background(), uuid.New(), invalid)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When managed metadata fields are set by the caller CreateRepositoryFromUntrusted should clear them before creation", func(t *testing.T) {
		h, repoStore, _ := newTestHandler()
		repo := newGitRepository("untrusted-repo", "https://example.com/repo.git")
		repo.Metadata.Owner = lo.ToPtr("someone")
		repo.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := CreateRepositoryFromUntrusted(context.Background(), h, uuid.New(), repo)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, repoStore.items["untrusted-repo"].Metadata.Owner)
		require.Nil(t, repoStore.items["untrusted-repo"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller CreateRepository (trusted) should preserve them", func(t *testing.T) {
		h, repoStore, _ := newTestHandler()
		repo := newGitRepository("trusted-repo", "https://example.com/repo.git")
		repo.Metadata.Owner = lo.ToPtr("someone")
		repo.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.CreateRepository(context.Background(), uuid.New(), repo)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(repoStore.items["trusted-repo"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(repoStore.items["trusted-repo"].Metadata.Generation))
	})
}

// ── ListRepositories / GetRepository ─────────────────────────────────────

func TestListRepositories(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, status := h.CreateRepository(ctx, orgId, newGitRepository("repo1", "https://example.com/1.git"))
	require.Equal(t, statusCreatedCode, status.Code)

	resp, status := h.ListRepositories(ctx, orgId, domain.ListRepositoriesParams{})
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, resp.Items, 1)
}

func TestGetRepository(t *testing.T) {
	t.Run("When the repository exists it should return it", func(t *testing.T) {
		h, _, _ := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, status := h.CreateRepository(ctx, orgId, newGitRepository("repo1", "https://example.com/1.git"))
		require.Equal(t, statusCreatedCode, status.Code)

		resp, status := h.GetRepository(ctx, orgId, "repo1")
		require.Equal(t, statusSuccessCode, status.Code)
		require.Equal(t, "repo1", *resp.Metadata.Name)
	})

	t.Run("When the repository does not exist it should return 404", func(t *testing.T) {
		h, _, _ := newTestHandler()
		_, status := h.GetRepository(context.Background(), uuid.New(), "missing")
		require.Equal(t, statusNotFoundCode, status.Code)
	})
}

// ── ReplaceRepository ─────────────────────────────────────────────────────

func TestReplaceRepository(t *testing.T) {
	t.Run("When the repository exists it should replace it and fire an updated callback", func(t *testing.T) {
		h, _, ev := newTestHandler()
		ctx := context.Background()
		orgId := uuid.New()
		_, status := h.CreateRepository(ctx, orgId, newGitRepository("repo1", "https://example.com/1.git"))
		require.Equal(t, statusCreatedCode, status.Code)

		updated := newGitRepository("repo1", "https://example.com/2.git")
		resp, status := h.ReplaceRepository(ctx, orgId, "repo1", updated)
		require.Equal(t, statusSuccessCode, status.Code)
		spec, err := resp.Spec.AsGitRepoSpec()
		require.NoError(t, err)
		require.Equal(t, "https://example.com/2.git", spec.Url)
		// Only the create produces a ResourceCreated event; replacing the URL alone
		// doesn't touch generation/labels/owner, so ComputeResourceUpdatedDetails sees
		// no change and no further event is emitted.
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When the name in metadata does not match the path it should return 400", func(t *testing.T) {
		h, _, _ := newTestHandler()
		repo := newGitRepository("repo1", "https://example.com/1.git")
		_, status := h.ReplaceRepository(context.Background(), uuid.New(), "other-name", repo)
		require.Equal(t, statusBadRequestCode, status.Code)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceRepositoryFromUntrusted should clear them before replacing", func(t *testing.T) {
		h, repoStore, _ := newTestHandler()
		orgId := uuid.New()
		repo := newGitRepository("replace-untrusted", "https://example.com/repo.git")
		repo.Metadata.Owner = lo.ToPtr("someone")
		repo.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := ReplaceRepositoryFromUntrusted(context.Background(), h, orgId, "replace-untrusted", repo)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Nil(t, repoStore.items["replace-untrusted"].Metadata.Owner)
		require.Nil(t, repoStore.items["replace-untrusted"].Metadata.Generation)
	})

	t.Run("When managed metadata fields are set by the caller ReplaceRepository (trusted) should preserve them", func(t *testing.T) {
		h, repoStore, _ := newTestHandler()
		orgId := uuid.New()
		repo := newGitRepository("replace-trusted", "https://example.com/repo.git")
		repo.Metadata.Owner = lo.ToPtr("someone")
		repo.Metadata.Generation = lo.ToPtr(int64(5))

		_, status := h.ReplaceRepository(context.Background(), orgId, "replace-trusted", repo)
		require.Equal(t, statusCreatedCode, status.Code)
		require.Equal(t, "someone", lo.FromPtr(repoStore.items["replace-trusted"].Metadata.Owner))
		require.Equal(t, int64(5), lo.FromPtr(repoStore.items["replace-trusted"].Metadata.Generation))
	})
}

// ── DeleteRepository ───────────────────────────────────────────────────────

func TestDeleteRepository(t *testing.T) {
	h, _, ev := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, status := h.CreateRepository(ctx, orgId, newGitRepository("repo1", "https://example.com/1.git"))
	require.Equal(t, statusCreatedCode, status.Code)

	status = h.DeleteRepository(ctx, orgId, "repo1")
	require.Equal(t, statusSuccessCode, status.Code)

	_, status = h.GetRepository(ctx, orgId, "repo1")
	require.Equal(t, statusNotFoundCode, status.Code)
	require.Len(t, ev.deleted, 1)
}

// ── PatchRepository ─────────────────────────────────────────────────────────

func testRepositoryPatch(t *testing.T, patch domain.PatchRequest) (*domain.Repository, domain.Repository, domain.Status) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	repo := domain.Repository{
		ApiVersion: "v1beta1",
		Kind:       "Repository",
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
	}
	_ = repo.Spec.FromGitRepoSpec(domain.GitRepoSpec{Url: "foo", Type: domain.GitRepoSpecTypeGit})

	_, status := h.CreateRepository(ctx, orgId, repo)
	require.Equal(t, statusCreatedCode, status.Code)
	resp, status := h.PatchRepository(ctx, orgId, "foo", patch)
	return resp, repo, status
}

func TestPatchRepositoryImmutableFields(t *testing.T) {
	cases := []struct {
		name  string
		patch domain.PatchRequest
	}{
		{
			name: "When replacing metadata.name it should return 400",
			patch: domain.PatchRequest{
				{Op: "replace", Path: "/metadata/name", Value: lo.ToPtr[interface{}]("bar")},
			},
		},
		{
			name: "When removing metadata.name it should return 400",
			patch: domain.PatchRequest{
				{Op: "remove", Path: "/metadata/name"},
			},
		},
		{
			name: "When replacing kind it should return 400",
			patch: domain.PatchRequest{
				{Op: "replace", Path: "/kind", Value: lo.ToPtr[interface{}]("bar")},
			},
		},
		{
			name: "When replacing apiVersion it should return 400",
			patch: domain.PatchRequest{
				{Op: "replace", Path: "/apiVersion", Value: lo.ToPtr[interface{}]("bar")},
			},
		},
		{
			name: "When removing spec it should return 400",
			patch: domain.PatchRequest{
				{Op: "remove", Path: "/spec"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, status := testRepositoryPatch(t, tc.patch)
			require.Equal(t, statusBadRequestCode, status.Code)
		})
	}
}

func TestPatchRepositoryLabels(t *testing.T) {
	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: lo.ToPtr[interface{}]("labelValue1")},
	}
	resp, orig, status := testRepositoryPatch(t, pr)
	orig.Metadata.Labels = &map[string]string{"labelKey": "labelValue1"}
	require.Equal(t, statusSuccessCode, status.Code)
	require.Equal(t, orig, *resp)
}

func TestPatchRepositoryNotFound(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, status := h.CreateRepository(ctx, orgId, newGitRepository("foo", "https://example.com/1.git"))
	require.Equal(t, statusCreatedCode, status.Code)

	pr := domain.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: lo.ToPtr[interface{}]("labelValue1")},
	}
	_, status = h.PatchRepository(ctx, orgId, "bar", pr)
	require.Equal(t, statusNotFoundCode, status.Code)
}

// ── ReplaceRepositoryStatusByError ──────────────────────────────────────────

func TestReplaceRepositoryStatusByError(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	repo := newGitRepository("repo1", "https://example.com/1.git")
	_, status := h.CreateRepository(ctx, orgId, repo)
	require.Equal(t, statusCreatedCode, status.Code)

	got, status := h.GetRepository(ctx, orgId, "repo1")
	require.Equal(t, statusSuccessCode, status.Code)
	// CreateRepository nils out Status (service-managed field), so the caller
	// (mirroring the tasks layer, e.g. the repo-tester) must initialize it
	// before calling ReplaceRepositoryStatusByError.
	got.Status = &domain.RepositoryStatus{Conditions: []domain.Condition{}}

	_, status = h.ReplaceRepositoryStatusByError(ctx, orgId, "repo1", *got, nil)
	require.Equal(t, statusSuccessCode, status.Code)
}

// ── GetRepositoryFleetReferences / GetRepositoryDeviceReferences ───────────

func TestGetRepositoryFleetReferences(t *testing.T) {
	h, repoStore, _ := newTestHandler()
	repoStore.fleetRefs["repo1"] = &domain.FleetList{Items: []domain.Fleet{{Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet1")}}}}

	resp, status := h.GetRepositoryFleetReferences(context.Background(), uuid.New(), "repo1")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, resp.Items, 1)
	require.Equal(t, "fleet1", *resp.Items[0].Metadata.Name)
}

func TestGetRepositoryDeviceReferences(t *testing.T) {
	h, repoStore, _ := newTestHandler()
	repoStore.deviceRefs["repo1"] = &domain.DeviceList{Items: []domain.Device{{Metadata: domain.ObjectMeta{Name: lo.ToPtr("dev1")}}}}

	resp, status := h.GetRepositoryDeviceReferences(context.Background(), uuid.New(), "repo1")
	require.Equal(t, statusSuccessCode, status.Code)
	require.Len(t, resp.Items, 1)
	require.Equal(t, "dev1", *resp.Items[0].Metadata.Name)
}

// ── CheckRepositoryOciTag / CheckRepositoryOciImage ─────────────────────────

func TestCheckRepositoryOciTagRejectsNonOciRepo(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, status := h.CreateRepository(ctx, orgId, newGitRepository("git-repo", "https://example.com/1.git"))
	require.Equal(t, statusCreatedCode, status.Code)

	_, status = h.CheckRepositoryOciTag(ctx, orgId, "git-repo", "myorg/myimage", "latest")
	require.Equal(t, statusBadRequestCode, status.Code)
}

func TestCheckRepositoryOciImageRejectsNonOciRepo(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()
	_, status := h.CreateRepository(ctx, orgId, newGitRepository("git-repo-2", "https://example.com/1.git"))
	require.Equal(t, statusCreatedCode, status.Code)

	_, status = h.CheckRepositoryOciImage(ctx, orgId, "git-repo-2", "quay.io/myorg/myimage")
	require.Equal(t, statusBadRequestCode, status.Code)
}

func TestCheckRepositoryOciTagRejectsNotFound(t *testing.T) {
	h, _, _ := newTestHandler()
	_, status := h.CheckRepositoryOciTag(context.Background(), uuid.New(), "nonexistent", "quay.io/myorg/myimage", "latest")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestCheckRepositoryOciImageRejectsNotFound(t *testing.T) {
	h, _, _ := newTestHandler()
	_, status := h.CheckRepositoryOciImage(context.Background(), uuid.New(), "nonexistent", "quay.io/myorg/myimage")
	require.Equal(t, statusNotFoundCode, status.Code)
}

func TestCheckRepositoryOciTagRejectsInvalidImageName(t *testing.T) {
	h, _, _ := newTestHandler()
	_, status := h.CheckRepositoryOciTag(context.Background(), uuid.New(), "any-repo", "quay.io/myorg/myimage:latest", "latest")
	require.Equal(t, statusBadRequestCode, status.Code)
}

func TestCheckRepositoryOciImageRejectsInvalidImageName(t *testing.T) {
	h, _, _ := newTestHandler()
	_, status := h.CheckRepositoryOciImage(context.Background(), uuid.New(), "any-repo", "quay.io/myorg/myimage:latest")
	require.Equal(t, statusBadRequestCode, status.Code)
}

// safePathRecorder is a goroutine-safe recorder for HTTP request paths captured
// by test servers running in their own goroutines.
type safePathRecorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *safePathRecorder) append(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *safePathRecorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.paths))
	copy(out, r.paths)
	return out
}

// newOciTestServer starts an httptest.Server that records every request path and
// returns 404 for all requests (simulating an unreachable image without crashing).
func newOciTestServer(t *testing.T) (*httptest.Server, *safePathRecorder) {
	t.Helper()
	rec := &safePathRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.append(r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func TestCheckRepositoryOciTagUsesRegistryFromSpec(t *testing.T) {
	h, _, _ := newTestHandler()
	ctx := context.Background()
	orgId := uuid.New()

	srv, paths := newOciTestServer(t)
	registry := strings.TrimPrefix(srv.URL, "http://")
	_, status := h.CreateRepository(ctx, orgId, newOciRepository("oci-repo", registry))
	require.Equal(t, statusCreatedCode, status.Code)

	result, status := h.CheckRepositoryOciTag(ctx, orgId, "oci-repo", "myorg/myimage", "latest")

	require.Equal(t, statusSuccessCode, status.Code)
	require.NotNil(t, result)
	require.False(t, result.Accessible)
	recorded := paths.get()
	require.NotEmpty(t, recorded, "expected ORAS to contact the registry from the spec")
	found := false
	for _, p := range recorded {
		if strings.Contains(p, "myorg/myimage") {
			found = true
			break
		}
	}
	require.True(t, found, "expected a request path containing 'myorg/myimage', got %v", recorded)
}
