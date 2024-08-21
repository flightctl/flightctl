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

type ResourceSyncStore struct {
	store.Store
	ResourceSyncVal v1alpha1.ResourceSync
}

func (s *ResourceSyncStore) ResourceSync() store.ResourceSync {
	return &DummyResourceSync{ResourceSyncVal: s.ResourceSyncVal}
}

type DummyResourceSync struct {
	store.ResourceSync
	ResourceSyncVal v1alpha1.ResourceSync
}

func (s *DummyResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*v1alpha1.ResourceSync, error) {
	if name == *s.ResourceSyncVal.Metadata.Name {
		return &s.ResourceSyncVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *v1alpha1.ResourceSync) (*v1alpha1.ResourceSync, error) {
	return resourceSync, nil
}

func (s *DummyResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *v1alpha1.ResourceSync) (*v1alpha1.ResourceSync, bool, error) {
	return resourceSync, false, nil
}

func verifyRSPatchFailed(require *require.Assertions, resp server.PatchResourceSyncResponseObject) {
	_, ok := resp.(server.PatchResourceSync400JSONResponse)
	require.True(ok)
}

func testResourceSyncPatch(require *require.Assertions, patch v1alpha1.PatchRequest) (server.PatchResourceSyncResponseObject, v1alpha1.ResourceSync) {
	resourceSync := v1alpha1.ResourceSync{
		ApiVersion: "v1",
		Kind:       "ResourceSync",
		Metadata: v1alpha1.ObjectMeta{
			Name:   util.StrToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: v1alpha1.ResourceSyncSpec{
			Repository: "foo",
		},
	}
	serviceHandler := ServiceHandler{
		store: &ResourceSyncStore{ResourceSyncVal: resourceSync},
	}
	resp, err := serviceHandler.PatchResourceSync(context.Background(), server.PatchResourceSyncRequestObject{
		Name: "foo",
		Body: &patch,
	})
	require.NoError(err)
	return resp, resourceSync
}
func TestResourceSyncPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)
}

func TestResourceSyncPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)
}

func TestResourceSyncPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)
}

func TestResourceSyncPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	var value interface{} = "bar"
	pr = v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/repository", Value: &value},
	}
	resp, rs := testResourceSyncPatch(require, pr)
	rs.Spec.Repository = "bar"
	require.Equal(server.PatchResourceSync200JSONResponse(rs), resp)
}

func TestResourceSyncPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	resp, _ = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)
}

func TestResourceSyncPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	resp, _ := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	resp, _ = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, resp)
}

func TestResourceSyncPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, resourceSync := testResourceSyncPatch(require, pr)
	resourceSync.Metadata.Labels = &addLabels
	require.Equal(server.PatchResourceSync200JSONResponse(resourceSync), resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, resourceSync = testResourceSyncPatch(require, pr)
	resourceSync.Metadata.Labels = &map[string]string{}
	require.Equal(server.PatchResourceSync200JSONResponse(resourceSync), resp)
}

func TestResourceSyncNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &ResourceSyncStore{ResourceSyncVal: v1alpha1.ResourceSync{
			Metadata: v1alpha1.ObjectMeta{Name: util.StrToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchResourceSync(context.Background(), server.PatchResourceSyncRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchResourceSync404JSONResponse{}, resp)
}
