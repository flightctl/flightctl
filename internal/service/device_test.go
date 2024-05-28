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

type DeviceStore struct {
	store.Store
	DeviceVal api.Device
}

func (s *DeviceStore) Device() store.Device {
	return &DummyDevice{DeviceVal: s.DeviceVal}
}

type DummyDevice struct {
	store.Device
	DeviceVal api.Device
}

func (s *DummyDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	if name == *s.DeviceVal.Metadata.Name {
		return &s.DeviceVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, callback store.DeviceStoreCallback) (*api.Device, bool, error) {
	return device, false, nil
}

func verifyDevicePatchFailed(require *require.Assertions, resp server.PatchDeviceResponseObject) {
	_, ok := resp.(server.PatchDevice400JSONResponse)
	require.True(ok)
}

func testDevicePatch(require *require.Assertions, patch api.PatchRequest) (server.PatchDeviceResponseObject, api.Device) {
	device := api.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: api.ObjectMeta{
			Name:   util.StrToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOSSpec{Image: "img"},
		},
		Status: &api.DeviceStatus{
			UpdatedAt: util.StrToPtr("123"),
		},
	}
	serviceHandler := ServiceHandler{
		store: &DeviceStore{DeviceVal: device},
	}
	resp, err := serviceHandler.PatchDevice(context.Background(), server.PatchDeviceRequestObject{
		Name: "foo",
		Body: &patch,
	})
	require.NoError(err)
	return resp, device
}
func TestDevicePatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	require.Equal(server.PatchDevice400JSONResponse{Message: "metadata.name is immutable"}, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

}

func TestDevicePatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newimg"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/image", Value: &value},
	}
	resp, device := testDevicePatch(require, pr)
	device.Spec.Os.Image = "newimg"
	require.Equal(server.PatchDevice200JSONResponse(device), resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os"},
	}
	resp, device = testDevicePatch(require, pr)
	device.Spec.Os = nil
	require.Equal(server.PatchDevice200JSONResponse(device), resp)

	value = "foo"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/os", Value: &value},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/status/updatedAt"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

}

func TestDevicePatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, device := testDevicePatch(require, pr)
	device.Metadata.Labels = &addLabels
	require.Equal(server.PatchDevice200JSONResponse(device), resp)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, device = testDevicePatch(require, pr)
	device.Metadata.Labels = &map[string]string{}
	require.Equal(server.PatchDevice200JSONResponse(device), resp)
}

func TestDeviceNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &DeviceStore{DeviceVal: api.Device{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchDevice(context.Background(), server.PatchDeviceRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchDevice404JSONResponse{}, resp)
}
