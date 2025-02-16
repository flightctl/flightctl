package service

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type DeviceStore struct {
	store.Store
	DeviceVal v1alpha1.Device
}

func (s *DeviceStore) Device() store.Device {
	return &DummyDevice{DeviceVal: s.DeviceVal}
}

type DummyDevice struct {
	store.Device
	DeviceVal v1alpha1.Device
}

type dummyPublisher struct{}

func (d *dummyPublisher) Publish(_ []byte) error {
	return nil
}

func (d *dummyPublisher) Close() {

}

func dummyCallbackManager() tasks_client.CallbackManager {
	return tasks_client.NewCallbackManager(&dummyPublisher{}, logrus.New())
}

func (s *DummyDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*v1alpha1.Device, error) {
	if name == *s.DeviceVal.Metadata.Name {
		return &s.DeviceVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) Update(ctx context.Context, orgId uuid.UUID, device *v1alpha1.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*v1alpha1.Device, error) {
	return device, nil
}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *v1alpha1.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*v1alpha1.Device, bool, error) {
	return device, false, nil
}

func verifyDevicePatchSucceeded(require *require.Assertions, expectedDevice *v1alpha1.Device, resp server.PatchDeviceResponseObject) {
	resp200, ok := resp.(server.PatchDevice200JSONResponse)
	require.True(ok)
	require.NotNil(resp200)
	actualDevice := (v1alpha1.Device)(resp200)
	// ignore fields updated by server-side status logic when testing equality
	actualDevice.Status.Summary = expectedDevice.Status.Summary
	actualDevice.Status.Updated = expectedDevice.Status.Updated
	actualDevice.Status.ApplicationsSummary = expectedDevice.Status.ApplicationsSummary
	require.Equal(server.PatchDevice200JSONResponse(actualDevice), resp)
}

func verifyDevicePatchFailed(require *require.Assertions, resp server.PatchDeviceResponseObject) {
	_, ok := resp.(server.PatchDevice400JSONResponse)
	require.True(ok)
}

func testDevicePatch(require *require.Assertions, patch v1alpha1.PatchRequest) (server.PatchDeviceResponseObject, v1alpha1.Device) {
	_ = os.Setenv(auth.DisableAuthEnvKey, "true")
	_, _ = auth.CreateAuthMiddleware(nil, log.InitLogs())
	status := v1alpha1.NewDeviceStatus()
	device := v1alpha1.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: v1alpha1.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: &v1alpha1.DeviceSpec{
			Os: &v1alpha1.DeviceOsSpec{Image: "img"},
		},
		Status: &status,
	}
	serviceHandler := ServiceHandler{
		store:           &DeviceStore{DeviceVal: device},
		callbackManager: dummyCallbackManager(),
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
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	require.Equal(server.PatchDevice400JSONResponse(v1alpha1.StatusBadRequest("metadata.name is immutable")), resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

}

func TestDevicePatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newimg"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/os/image", Value: &value},
	}
	resp, device := testDevicePatch(require, pr)
	device.Spec.Os.Image = "newimg"
	verifyDevicePatchSucceeded(require, &device, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec/os"},
	}
	resp, device = testDevicePatch(require, pr)
	device.Spec.Os = nil
	verifyDevicePatchSucceeded(require, &device, resp)

	value = "foo"
	pr = v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/os", Value: &value},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/status/updatedAt"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

}

func TestDevicePatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	resp, _ := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	resp, _ = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, resp)
}

func TestDevicePatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, device := testDevicePatch(require, pr)
	device.Metadata.Labels = &addLabels
	verifyDevicePatchSucceeded(require, &device, resp)

	pr = v1alpha1.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, device = testDevicePatch(require, pr)
	device.Metadata.Labels = &map[string]string{}
	verifyDevicePatchSucceeded(require, &device, resp)
}

func TestDeviceNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := v1alpha1.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &DeviceStore{DeviceVal: v1alpha1.Device{
			Metadata: v1alpha1.ObjectMeta{Name: lo.ToPtr("foo")},
		}},
	}
	resp, err := serviceHandler.PatchDevice(context.Background(), server.PatchDeviceRequestObject{
		Name: "bar",
		Body: &pr,
	})
	require.NoError(err)
	require.Equal(server.PatchDevice404JSONResponse(v1alpha1.StatusResourceNotFound("Device", "bar")), resp)
}
