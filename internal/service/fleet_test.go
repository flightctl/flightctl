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

func (s *DummyFleet) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, error) {
	if name == *s.FleetVal.Metadata.Name {
		return &s.FleetVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyFleet) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback store.FleetStoreCallback) (*api.Fleet, bool, error) {
	return fleet, false, nil
}

func verifyFleetPatchFailed(require *require.Assertions, resp server.PatchFleetResponseObject) {
	_, ok := resp.(server.PatchFleet400JSONResponse)
	require.True(ok)
}

func testFleetPatch(require *require.Assertions, patch api.PatchRequest) (server.PatchFleetResponseObject, api.Fleet) {
	fleet := api.Fleet{
		ApiVersion: "v1",
		Kind:       "Fleet",
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: map[string]string{"devKey": "devValue"},
			},
			Template: struct {
				Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
				Spec     api.DeviceSpec  "json:\"spec\""
			}{
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: "img",
					},
				},
			},
		},
		Status: &api.FleetStatus{
			Conditions: &[]api.Condition{
				{
					Type:   "Approved",
					Status: "True",
				},
			},
		},
	}
	serviceHandler := ServiceHandler{
		store: &FleetStore{FleetVal: fleet},
	}
	resp, err := serviceHandler.PatchFleet(context.Background(), server.PatchFleetRequestObject{
		Name: "foo",
		Body: &patch,
	})
	require.NoError(err)
	return resp, fleet
}
func TestFleetPatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newValue"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	resp, device := testFleetPatch(require, pr)
	device.Spec.Selector.MatchLabels = map[string]string{"devKey": "newValue"}
	require.Equal(server.PatchFleet200JSONResponse(device), resp)

	value = 1234
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/selector/matchLabels/devKey", Value: &value},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)

	value = "newimg"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os/image", Value: &value},
	}
	resp, device = testFleetPatch(require, pr)
	device.Spec.Template = struct {
		Metadata *api.ObjectMeta "json:\"metadata,omitempty\""
		Spec     api.DeviceSpec  "json:\"spec\""
	}{
		Spec: api.DeviceSpec{
			Os: &api.DeviceOSSpec{
				Image: "newimg",
			},
		},
	}
	device.Spec.Template.Spec.Os.Image = "newimg"
	require.Equal(server.PatchFleet200JSONResponse(device), resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/template/spec/os"},
	}
	resp, device = testFleetPatch(require, pr)
	device.Spec.Template.Spec.Os = nil
	require.Equal(server.PatchFleet200JSONResponse(device), resp)

	value = "foo"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/template/spec/os", Value: &value},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchStatus(t *testing.T) {
	require := require.New(t)
	pr := api.PatchRequest{
		{Op: "remove", Path: "/status/conditions/0"},
	}
	resp, _ := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/doesnotexist", Value: &value},
	}
	resp, _ := testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/doesnotexist"},
	}
	resp, _ = testFleetPatch(require, pr)
	verifyFleetPatchFailed(require, resp)
}

func TestFleetPatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, device := testFleetPatch(require, pr)
	device.Metadata.Labels = &addLabels
	require.Equal(server.PatchFleet200JSONResponse(device), resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, device = testFleetPatch(require, pr)
	device.Metadata.Labels = &map[string]string{}
	require.Equal(server.PatchFleet200JSONResponse(device), resp)
}

func TestFleetNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &FleetStore{FleetVal: api.Fleet{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchFleet(context.Background(), server.PatchFleetRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchFleet404JSONResponse{}, resp)
}
