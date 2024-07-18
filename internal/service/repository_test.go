package service

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type RepositoryStore struct {
	store.Store
	RepositoryVal v1alpha1.Repository
}

func (s *RepositoryStore) Repository() store.Repository {
	return &DummyRepository{RepositoryVal: s.RepositoryVal}
}

type DummyRepository struct {
	store.Repository
	RepositoryVal v1alpha1.Repository
}

func (s *DummyRepository) Get(ctx context.Context, orgId uuid.UUID, name string) (*v1alpha1.Repository, error) {
	if name == *s.RepositoryVal.Metadata.Name {
		return &s.RepositoryVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyRepository) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, repository *v1alpha1.Repository, callback store.RepositoryStoreCallback) (*v1alpha1.Repository, bool, error) {
	return repository, false, nil
}

func verifyRepoPatchFailed(require *require.Assertions, resp server.PatchRepositoryResponseObject) {
	_, ok := resp.(server.PatchRepository400JSONResponse)
	require.True(ok)
}

func testRepositoryPatch(require *require.Assertions, patch v1alpha1.PatchRequest) (server.PatchRepositoryResponseObject, v1alpha1.Repository) {
	spec := v1alpha1.RepositorySpec{}
	err := spec.FromGenericRepoSpec(v1alpha1.GenericRepoSpec{
		Url:  "foo",
		Type: "git",
	})
	require.NoError(err)
	repository := v1alpha1.Repository{
		ApiVersion: "v1",
		Kind:       "Repository",
		Metadata: v1alpha1.ObjectMeta{
			Name:   util.StrToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: spec,
	}
	serviceHandler := ServiceHandler{
		store:           &RepositoryStore{RepositoryVal: repository},
		callbackManager: dummyCallbackManager(),
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
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	resp, _ := testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	resp, _ = testRepositoryPatch(require, pr)
	verifyRepoPatchFailed(require, resp)
}

func TestRepositoryPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, repository := testRepositoryPatch(require, pr)
	repository.Metadata.Labels = &addLabels
	require.Equal(server.PatchRepository200JSONResponse(repository), resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, repository = testRepositoryPatch(require, pr)
	repository.Metadata.Labels = &map[string]string{}
	require.Equal(server.PatchRepository200JSONResponse(repository), resp)
}

func TestRepositoryNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &RepositoryStore{RepositoryVal: v1alpha1.Repository{
			Metadata: v1alpha1.ObjectMeta{Name: util.StrToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchRepository(context.Background(), server.PatchRepositoryRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchRepository404JSONResponse{}, resp)
}
