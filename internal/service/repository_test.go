package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type RepositoryStore struct {
	store.Store
	RepositoryVal api.Repository
}

func (s *RepositoryStore) Repository() store.Repository {
	return &DummyRepository{RepositoryVal: s.RepositoryVal}
}

type DummyRepository struct {
	store.Repository
	RepositoryVal api.Repository
}

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, error) {
	if name == *s.RepositoryVal.Metadata.Name {
		return &s.RepositoryVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) Update(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, error) {
	return repository, nil
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, bool, error) {
	return repository, false, nil
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
