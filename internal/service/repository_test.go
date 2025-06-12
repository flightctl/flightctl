package service

import (
	"context"
	"errors"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type RepositoryStore struct {
	store.Store
	RepositoryVal   api.Repository
	EventVal        api.Event
	ReturnErr       error
	DummyRepository *DummyRepository
}

func (s *RepositoryStore) Repository() store.Repository {
	repo := DummyRepository{RepositoryVal: s.RepositoryVal, ReturnErr: s.ReturnErr}
	s.DummyRepository = &repo
	return s.DummyRepository
}

func (s *RepositoryStore) Event() store.Event {
	return &DummyEvent{EventVal: s.EventVal}
}

type DummyRepository struct {
	store.Repository
	RepositoryVal api.Repository
	ReturnErr     error
	CalledOrgId   uuid.UUID
}

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	if name == *s.RepositoryVal.Metadata.Name {
		return &s.RepositoryVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, api.ResourceUpdatedDetails, error) {
	return repository, api.ResourceUpdatedDetails{}, nil
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, bool, api.ResourceUpdatedDetails, error) {
	return repository, false, api.ResourceUpdatedDetails{}, nil
}

func (s *DummyRepository) List(ctx context.Context, orgId uuid.UUID, params store.ListParams) (*api.RepositoryList, error) {
	if s.ReturnErr != nil {
		return nil, s.ReturnErr
	}
	s.CalledOrgId = orgId
	return &api.RepositoryList{
		Kind:  "RepositoryList",
		Items: []api.Repository{s.RepositoryVal},
	}, nil
}

func verifyRepoPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(int32(400), status.Code)
}

func testRepositoryPatch(require *require.Assertions, patch api.PatchRequest) (*api.Repository, api.Repository, api.Status) {
	spec := api.RepositorySpec{}
	err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
		Url:  "foo",
		Type: "git",
	})
	require.NoError(err)
	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: spec,
	}
	serviceHandler := ServiceHandler{
		store:           &RepositoryStore{RepositoryVal: repository},
		callbackManager: dummyCallbackManager(),
	}
	resp, status := serviceHandler.PatchRepository(context.Background(), "foo", patch)
	require.NotEqual(int32(500), status.Code)
	return resp, repository, status
}

func TestRepositoryPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	_, _, status := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	_, _, status = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, status)
}

func TestRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testRepositoryPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)
}

func TestRepositoryNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: api.Repository{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
		}},
	}
	_, status := serviceHandler.PatchRepository(context.Background(), "bar", pr)
	require.Equal(int32(404), status.Code)
}

func TestListRepositoriesNoOrgID(t *testing.T) {
	require := require.New(t)

	repository := api.Repository{
		Kind: api.RepositoryKind,
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-repo"),
		},
	}
	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: repository},
	}

	_, status := serviceHandler.ListRepositories(context.Background(), api.ListRepositoriesParams{})
	require.Equal(int32(400), status.Code)
	require.Equal(flterrors.ErrOrgIDInvalid.Error(), status.Message)
}

func TestListRepositoriesStoreError(t *testing.T) {
	require := require.New(t)

	repository := api.Repository{
		Kind: api.RepositoryKind,
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-repo"),
		},
	}
	expectedErr := errors.New("OH NO!")
	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: repository, ReturnErr: expectedErr},
	}

	ctx := context.Background()
	ctx = util.WithOrganizationID(ctx, uuid.New())
	_, status := serviceHandler.ListRepositories(ctx, api.ListRepositoriesParams{})
	require.Equal(int32(500), status.Code)
	require.Equal(expectedErr.Error(), status.Message)
}

func TestListRepositories(t *testing.T) {
	require := require.New(t)

	repository := api.Repository{
		Kind: api.RepositoryKind,
		Metadata: api.ObjectMeta{
			Name: lo.ToPtr("test-repo"),
		},
	}
	repoStore := &RepositoryStore{RepositoryVal: repository}
	serviceHandler := ServiceHandler{
		store: repoStore,
	}
	ctx := context.Background()
	expectedOrgID := uuid.New()
	ctx = util.WithOrganizationID(ctx, expectedOrgID)
	resp, status := serviceHandler.ListRepositories(ctx, api.ListRepositoriesParams{})
	require.Equal(int32(200), status.Code)
	require.Equal(expectedOrgID, repoStore.DummyRepository.CalledOrgId)
	require.Equal(repository, resp.Items[0])
}
