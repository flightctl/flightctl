package service

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func verifyRSPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func testResourceSyncPatch(require *require.Assertions, patch api.PatchRequest) (*api.ResourceSync, api.ResourceSync, api.Status) {
	ctx := context.Background()
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
		store: &TestStore{},
	}
	orig, status := serviceHandler.CreateResourceSync(ctx, resourceSync)
	require.Equal(statusCreatedCode, status.Code)
	resp, status := serviceHandler.PatchResourceSync(ctx, "foo", patch)
	require.NotEqual(statusFailedCode, status.Code)
	event, _ := serviceHandler.store.Event().List(context.Background(), store.NullOrgId, store.ListParams{})
	require.NotEmpty(event.Items)
	return resp, *orig, status
}

func TestResourceSyncCreateWithLongNames(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()

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
		store: &TestStore{},
	}
	_, err := serviceHandler.store.ResourceSync().Create(ctx, store.NullOrgId, &resourceSync)
	require.NoError(err)
	_, status := serviceHandler.ReplaceResourceSync(ctx,
		"01234567890123456789012345678901234567890123456789012345678901234567890123456789",
		resourceSync,
	)
	require.Equal(statusSuccessCode, status.Code)
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
	require.Equal(statusSuccessCode, status.Code)
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
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testResourceSyncPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(statusSuccessCode, status.Code)
	require.Equal(orig, *resp)
}

func TestResourceSyncNonExistingResource(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &TestStore{},
	}
	_, err := serviceHandler.store.ResourceSync().Create(ctx, store.NullOrgId, &api.ResourceSync{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
	})
	require.NoError(err)
	_, status := serviceHandler.PatchResourceSync(ctx, "bar", pr)
	require.Equal(statusNotFoundCode, status.Code)
	event, _ := serviceHandler.store.Event().List(context.Background(), store.NullOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}
