package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
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

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *api.Repository, callback store.RepositoryStoreCallback) (*api.Repository, bool, error) {
	return repository, false, nil
}

func verifyRepoPatchFailed(require *require.Assertions, resp server.PatchRepositoryResponseObject) {
	_, ok := resp.(server.PatchRepository400JSONResponse)
	require.True(ok)
}

func testRepositoryPatch(require *require.Assertions, patch api.PatchRequest) (server.PatchRepositoryResponseObject, api.Repository) {
	spec := api.RepositorySpec{}
	err := spec.FromGitGenericRepoSpec(api.GitGenericRepoSpec{
		Repo: "foo",
	})
	require.NoError(err)
	repository := api.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: spec,
	}
	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: repository},
	}
	resp, err := serviceHandler.PatchRepository(context.Background(), server.PatchRepositoryRequestObject{
		Name: "foo",
		Body: &patch,
	})
	require.NoError(err)
	return resp, repository
}
func TestRepositoryPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, repository := testRepositoryPatch(require, pr)
	repository.Metadata.Labels = &addLabels
	require.Equal(server.PatchRepository200JSONResponse(repository), resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, repository = testRepositoryPatch(require, pr)
	repository.Metadata.Labels = &map[string]string{}
	require.Equal(server.PatchRepository200JSONResponse(repository), resp)
}

func TestRepositoryNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: api.Repository{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchRepository(context.Background(), server.PatchRepositoryRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchRepository404JSONResponse{}, resp)
}
