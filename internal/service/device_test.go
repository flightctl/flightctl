package service

import (
	"context"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type DeviceStore struct {
	store.Store
	DeviceVal api.Device
	EventVal  api.Event
}

func (s *DeviceStore) Device() store.Device {
	return &DummyDevice{DeviceVal: s.DeviceVal}
}

type DummyDevice struct {
	store.Device
	DeviceVal api.Device
}

func (s *DeviceStore) Event() store.Event {
	return &DummyEvent{EventVal: s.EventVal}
}

type DummyEvent struct {
	store.Event
	EventVal api.Event
}

type dummyPublisher struct{}

func (d *dummyPublisher) Publish(_ context.Context, _ []byte) error {
	return nil
}

func (d *dummyPublisher) Close() {

}

func dummyCallbackManager() tasks_client.CallbackManager {
	return tasks_client.NewCallbackManager(&dummyPublisher{}, logrus.New())
}

func (s *DummyDevice) Get(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, error) {
	if name == *s.DeviceVal.Metadata.Name {
		return &s.DeviceVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyDevice) Update(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, api.ResourceUpdatedDetails, error) {
	return device, api.ResourceUpdatedDetails{}, nil
}

func (s *DummyDevice) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, device *api.Device, fieldsToUnset []string, fromAPI bool, validationCallback store.DeviceStoreValidationCallback, callback store.DeviceStoreCallback) (*api.Device, bool, api.ResourceUpdatedDetails, error) {
	return device, false, api.ResourceUpdatedDetails{}, nil
}

func (s *DummyEvent) Create(ctx context.Context, orgId uuid.UUID, event *api.Event) error {
	return nil
}

func verifyDevicePatchSucceeded(require *require.Assertions, expectedDevice api.Device, resp *api.Device, status api.Status) {
	require.Equal(int32(200), status.Code)
	require.True(api.DeviceSpecsAreEqual(*expectedDevice.Spec, *resp.Spec))
	require.Equal(expectedDevice.Metadata, resp.Metadata)
}

func verifyDevicePatchFailed(require *require.Assertions, status api.Status) {
	require.Equal(int32(400), status.Code)
}

func testDevicePatch(require *require.Assertions, patch api.PatchRequest) (*api.Device, api.Device, api.Status) {
	_ = os.Setenv(auth.DisableAuthEnvKey, "true")
	_ = auth.InitAuth(nil, log.InitLogs())
	status := api.NewDeviceStatus()
	device := api.Device{
		ApiVersion: "v1",
		Kind:       "Device",
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr("foo"),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: &api.DeviceSpec{
			Os: &api.DeviceOsSpec{Image: "img"},
		},
		Status: &status,
	}
	serviceHandler := ServiceHandler{
		store:           &DeviceStore{DeviceVal: device},
		callbackManager: dummyCallbackManager(),
	}
	resp, retStatus := serviceHandler.PatchDevice(context.Background(), "foo", patch)
	require.NotEqual(int32(500), retStatus.Code)
	return resp, device, retStatus
}

func testDeviceStatusPatch(require *require.Assertions, orig api.Device, patch api.PatchRequest) (*api.Device, api.Status) {
	_ = os.Setenv(auth.DisableAuthEnvKey, "true")
	_ = auth.InitAuth(nil, log.InitLogs())
	serviceHandler := ServiceHandler{
		store:           &DeviceStore{DeviceVal: orig},
		callbackManager: dummyCallbackManager(),
	}
	resp, retStatus := serviceHandler.PatchDeviceStatus(context.Background(), "foo", patch)
	require.NotEqual(int32(500), retStatus.Code)
	return resp, retStatus
}

func TestDevicePatchName(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/name", Value: &value},
	}
	_, _, status := testDevicePatch(require, pr)
	require.Equal(api.StatusBadRequest("metadata.name is immutable"), status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/name"},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)
}

func TestDeviceStatusPatch(t *testing.T) {
	require := require.New(t)
	testCases := []struct {
		name               string
		patchPath          string
		patchValueType     string // "systemInfo" or "string"
		systemInfo         api.DeviceSystemInfo
		stringValue        string
		expectedCode       int32
		expectedSystemInfo *api.DeviceSystemInfo
		expectError        bool
		errorMessage       string
	}{
		{
			name:           "update system info happy path",
			patchPath:      "/status/systemInfo",
			patchValueType: "systemInfo",
			systemInfo: api.DeviceSystemInfo{
				AgentVersion:    "a",
				Architecture:    "b",
				BootID:          "c",
				OperatingSystem: "d",
			},
			expectedCode: 200,
			expectedSystemInfo: &api.DeviceSystemInfo{
				AgentVersion:    "a",
				Architecture:    "b",
				BootID:          "c",
				OperatingSystem: "d",
			},
		},
		{
			name:           "update system info partial",
			patchPath:      "/status/systemInfo",
			patchValueType: "systemInfo",
			systemInfo: api.DeviceSystemInfo{
				AgentVersion:    "a",
				Architecture:    "b",
				BootID:          "3",
				OperatingSystem: "4",
			},
			expectedCode: 200,
			expectedSystemInfo: &api.DeviceSystemInfo{
				AgentVersion:    "a",
				Architecture:    "b",
				BootID:          "3",
				OperatingSystem: "4",
			},
		},
		{
			name:           "attempt to patch metadata name should fail",
			patchPath:      "/metadata/name",
			patchValueType: "string",
			stringValue:    "newname",
			expectedCode:   400,
			expectError:    true,
			errorMessage:   "metadata is immutable",
		},
		{
			name:           "attempt to patch metadata owner should fail",
			patchPath:      "/metadata/owner",
			patchValueType: "string",
			stringValue:    "ozzy",
			expectedCode:   400,
			expectError:    true,
			errorMessage:   "metadata is immutable",
		},
		{
			name:           "attempt to patch spec should fail",
			patchPath:      "/spec/os/image",
			patchValueType: "string",
			stringValue:    "newimg",
			expectedCode:   400,
			expectError:    true,
			errorMessage:   "spec is immutable",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			deviceStatus := api.NewDeviceStatus()
			deviceStatus.SystemInfo = api.DeviceSystemInfo{
				AgentVersion:    "1",
				Architecture:    "2",
				BootID:          "3",
				OperatingSystem: "4",
			}

			// initialize device
			device := api.Device{
				ApiVersion: "v1",
				Kind:       "Device",
				Metadata: api.ObjectMeta{
					Name:   lo.ToPtr("foo"),
					Labels: &map[string]string{"labelKey": "labelValue"},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "img"},
				},
				Status: &deviceStatus,
			}

			// create patch request
			var patchRequest api.PatchRequest
			if tc.patchValueType == "systemInfo" {
				infoMap, err := util.StructToMap(tc.systemInfo)
				require.NoError(err)

				var value interface{} = infoMap
				patchRequest = api.PatchRequest{
					{Op: "replace", Path: tc.patchPath, Value: &value},
				}
			} else {
				var value interface{} = tc.stringValue
				patchRequest = api.PatchRequest{
					{Op: "replace", Path: tc.patchPath, Value: &value},
				}
			}

			resp, status := testDeviceStatusPatch(require, device, patchRequest)
			require.Equal(tc.expectedCode, status.Code)

			if tc.expectError {
				require.Equal(api.StatusBadRequest(tc.errorMessage), status)
			} else {
				require.Equal(*tc.expectedSystemInfo, resp.Status.SystemInfo)
			}
		})
	}
}

func TestDevicePatchKind(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/kind", Value: &value},
	}
	_, _, status := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/kind"},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)
}

func TestDevicePatchAPIVersion(t *testing.T) {
	require := require.New(t)
	var value interface{} = "bar"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/apiVersion", Value: &value},
	}
	_, _, status := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/apiVersion"},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

}

func TestDevicePatchSpec(t *testing.T) {
	require := require.New(t)
	var value interface{} = "newimg"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/image", Value: &value},
	}
	resp, orig, status := testDevicePatch(require, pr)
	orig.Spec.Os.Image = "newimg"
	verifyDevicePatchSucceeded(require, orig, resp, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os"},
	}
	resp, orig, status = testDevicePatch(require, pr)
	orig.Spec.Os = nil
	verifyDevicePatchSucceeded(require, orig, resp, status)

	value = "foo"
	pr = api.PatchRequest{
		{Op: "replace", Path: "/spec/os", Value: &value},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)
}

func TestDevicePatchStatus(t *testing.T) {
	require := require.New(t)
	var value interface{} = "1234"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/status/updatedAt", Value: &value},
	}
	_, _, status := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/status/updatedAt"},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

}

func TestDevicePatchNonExistingPath(t *testing.T) {
	require := require.New(t)
	var value interface{} = "foo"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/spec/os/doesnotexist", Value: &value},
	}
	_, _, status := testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/spec/os/doesnotexist"},
	}
	_, _, status = testDevicePatch(require, pr)
	verifyDevicePatchFailed(require, status)
}

func TestDevicePatchLabels(t *testing.T) {
	require := require.New(t)
	addLabels := map[string]string{"labelKey": "labelValue1"}
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	resp, orig, status := testDevicePatch(require, pr)
	orig.Metadata.Labels = &addLabels
	verifyDevicePatchSucceeded(require, orig, resp, status)

	pr = api.PatchRequest{
		{Op: "remove", Path: "/metadata/labels/labelKey"},
	}

	resp, orig, status = testDevicePatch(require, pr)
	orig.Metadata.Labels = &map[string]string{}
	verifyDevicePatchSucceeded(require, orig, resp, status)
}

func TestDeviceNonExistingResource(t *testing.T) {
	require := require.New(t)
	var value interface{} = "labelValue1"
	pr := api.PatchRequest{
		{Op: "replace", Path: "/metadata/labels/labelKey", Value: &value},
	}

	serviceHandler := ServiceHandler{
		store: &DeviceStore{DeviceVal: api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("foo")},
		}},
	}
	_, status := serviceHandler.PatchDevice(context.Background(), "bar", pr)
	require.Equal(api.StatusResourceNotFound("Device", "bar"), status)
}
