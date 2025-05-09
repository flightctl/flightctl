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

type ResourceSyncStore struct {
	store.Store
	ResourceSyncVal api.ResourceSync
	EventVal        api.Event
}

func (s *ResourceSyncStore) ResourceSync() store.ResourceSync {
	return &DummyResourceSync{ResourceSyncVal: s.ResourceSyncVal}
}

func (s *ResourceSyncStore) Event() store.Event {
	return &DummyEvent{EventVal: s.EventVal}
}

type DummyResourceSync struct {
	store.ResourceSync
	ResourceSyncVal api.ResourceSync
}

func (s *DummyResourceSync) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, error) {
	if name == *s.ResourceSyncVal.Metadata.Name {
		return &s.ResourceSyncVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyResourceSync) Update(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, api.ResourceUpdatedDetails, error) {
	return resourceSync, api.ResourceUpdatedDetails{}, nil
}

func (s *DummyResourceSync) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resourceSync *api.ResourceSync) (*api.ResourceSync, bool, api.ResourceUpdatedDetails, error) {
	return resourceSync, false, api.ResourceUpdatedDetails{}, nil
}

func verifyRSPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(int32(400), status.Code)
}

func testResourceSyncPatch(require *require.Assertions, patch api.PatchRequest) (*api.ResourceSync, api.ResourceSync, api.Status) {
	resourceSync := api.ResourceSync{
		ApiVersion: "v1",
		Kind:       "ResourceSync",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.ResourceSyncSpec{
			Repository:     "foo",
			TargetRevision: "main",
		},
	}
	serviceHandler := ServiceHandler{
		store: &ResourceSyncStore{ResourceSyncVal: resourceSync},
	}
	resp, status := serviceHandler.PatchResourceSync(context.Background(), "foo", patch)
	require.NotEqual(int32(500), status.Code)
	return resp, resourceSync, status
}

func TestResourceSyncCreateWithLongNames(t *testing.T) {
	require := require.New(t)

	resourceSync := api.ResourceSync{
		ApiVersion: "v1",
		Kind:       "ResourceSync",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("01234567890123456789012345678901234567890123456789012345678901234567890123456789"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.ResourceSyncSpec{
			Repository:     "01234567890123456789012345678901234567890123456789012345678901234567890123456789",
			TargetRevision: "main",
			Path:           "/foo",
		},
	}

	serviceHandler := ServiceHandler{
		store: &ResourceSyncStore{ResourceSyncVal: resourceSync},
	}
	_, status := serviceHandler.ReplaceResourceSync(context.Background(),
		"01234567890123456789012345678901234567890123456789012345678901234567890123456789",
		resourceSync,
	)
	require.Equal(int32(200), status.Code)
}

func TestResourceSyncPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)
}

func TestResourceSyncPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)
}

func TestResourceSyncPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)
}

func TestResourceSyncPatchSpec(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/spec"},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	var value interface{} = "bar"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/repository", Value: &value},
	}
	resp, orig, status := testResourceSyncPatch(require, pr)
	orig.Spec.Repository = "bar"
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)
}

func TestResourceSyncPatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt"},
	}
	_, _, status = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)
}

func TestResourceSyncPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	_, _, status = testResourceSyncPatch(require, pr)
	verifyRSPatchFailed(require, status)
}

func TestResourceSyncPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testResourceSyncPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testResourceSyncPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)
}

func TestResourceSyncNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &ResourceSyncStore{ResourceSyncVal: api.ResourceSync{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
		}},
	}
	_, status := serviceHandler.PatchResourceSync(context.Background(), "bar", pr)
	require.Equal(int32(404), status.Code)
}
