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

type FleetStore struct {
	store.Store
	FleetVal api.Fleet
}

func (s *FleetStore) Fleet() store.Fleet {
	return &DummyFleet{FleetVal: s.FleetVal}
}

type DummyFleet struct {
	store.Fleet
	FleetVal api.Fleet
}

func (s *DummyFleet) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...store.GetOption) (*api.Fleet, error) {
	if name == *s.FleetVal.Metadata.Name {
		return &s.FleetVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyFleet) Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, error) {
	return fleet, nil
}

func (s *DummyFleet) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback store.FleetStoreCallback) (*api.Fleet, bool, error) {
	return fleet, false, nil
}

func verifyFleetPatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(int32(400), status.Code)
}

func testFleetPatch(require *require.Assertions, patch api.PatchRequest) (*api.Fleet, api.Fleet, api.Status) {
	fleet := api.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: &map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
				Spec     api.DeviceSpec  "json:\"spec\""
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: "img",
					},
				},
			},
		},
		Status: &api.FleetStatus{
			Conditions: []api.Condition{
				{
					Type:   "Approved",
					Status: "True",
				},
			},
		},
	}
	serviceHandler := ServiceHandler{
		store:           &FleetStore{FleetVal: fleet},
		callbackManager: dummyCallbackManager(),
	}
	resp, status := serviceHandler.PatchFleet(context.Background(), "foo", patch)
	require.NotEqual(int32(500), status.Code)
	return resp, fleet, status
}
func TestFleetPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newValue"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	_, _, status := testResourceSyncPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	value = 1234
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	value = "newimg"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os/image", Value: &value},
	}
	resp, orig, status := testFleetPatch(require, pr)
	orig.Spec.Template = struct {
		Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
		Spec     api.DeviceSpec  "json:\"spec\""
	}{
		Spec: api.DeviceSpec{
			Os: &api.DeviceOsSpec{
				Image: "newimg",
			},
		},
	}
	orig.Spec.Template.Spec.Os.Image = "newimg"
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/template/spec/os"},
	}
	resp, orig, status = testFleetPatch(require, pr)
	orig.Spec.Template.Spec.Os = nil
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)

	value = "foo"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os", Value: &value},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchStatus(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/status/conditions/0"},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/doesnotexist", Value: &value},
	}
	_, _, status := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/doesnotexist"},
	}
	_, _, status = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, status)
}

func TestFleetPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testFleetPatch(require, pr)
	orig.Metadata.Labels = &addLabels
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testFleetPatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	require.Equal(int32(200), status.Code)
	require.Equal(orig, *resp)
}

func TestFleetNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &FleetStore{FleetVal: api.Fleet{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
		}},
	}
	_, status := serviceHandler.PatchFleet(context.Background(), "bar", pr)
	require.Equal(int32(404), status.Code)
}
