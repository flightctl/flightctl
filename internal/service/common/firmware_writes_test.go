package common

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreserveOmittedFirmware(t *testing.T) {
	stored := &domain.DeviceSpec{Firmware: newTestFirmware(t)}

	t.Run("When the incoming spec omits firmware it should keep the stored firmware without modifying the incoming spec", func(t *testing.T) {
		incoming := &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/example/os:2"}}

		effective := PreserveOmittedFirmware(incoming, stored)

		require.NotNil(t, effective)
		assert.Same(t, stored.Firmware, effective.Firmware)
		assert.Equal(t, incoming.Os, effective.Os)
		assert.Nil(t, incoming.Firmware)
	})

	t.Run("When the incoming spec is absent it should keep only the stored firmware", func(t *testing.T) {
		effective := PreserveOmittedFirmware(nil, stored)

		require.NotNil(t, effective)
		assert.Same(t, stored.Firmware, effective.Firmware)
		assert.Nil(t, effective.Os)
	})

	t.Run("When the incoming spec declares firmware it should be used unchanged", func(t *testing.T) {
		incoming := &domain.DeviceSpec{Firmware: newTestFirmware(t)}
		incoming.Firmware.Profile = "other-profile"

		assert.Same(t, incoming, PreserveOmittedFirmware(incoming, stored))
	})

	t.Run("When nothing is stored it should return the incoming spec", func(t *testing.T) {
		incoming := &domain.DeviceSpec{}

		assert.Same(t, incoming, PreserveOmittedFirmware(incoming, nil))
		assert.Same(t, incoming, PreserveOmittedFirmware(incoming, &domain.DeviceSpec{}))
		assert.Nil(t, PreserveOmittedFirmware(nil, &domain.DeviceSpec{}))
	})
}

func TestFirmwareRemovedByPatch(t *testing.T) {
	tests := []struct {
		name  string
		patch domain.PatchRequest
		want  bool
	}{
		{
			name:  "When the request removes the firmware path it should report a withdrawal",
			patch: domain.PatchRequest{{Op: "remove", Path: DeviceFirmwarePatchPath, Value: nil}},
			want:  true,
		},
		{
			name: "When the request removes the firmware path among other operations it should report a withdrawal",
			patch: domain.PatchRequest{
				{Op: "add", Path: "/metadata/labels/site", Value: "lab"},
				{Op: "remove", Path: DeviceFirmwarePatchPath, Value: nil},
			},
			want: true,
		},
		{
			name:  "When the request replaces the firmware with null it should not report a withdrawal",
			patch: domain.PatchRequest{{Op: "replace", Path: DeviceFirmwarePatchPath, Value: nil}},
		},
		{
			name:  "When the request adds null firmware it should not report a withdrawal",
			patch: domain.PatchRequest{{Op: "add", Path: DeviceFirmwarePatchPath, Value: nil}},
		},
		{
			name:  "When the request removes the whole spec it should not report a withdrawal",
			patch: domain.PatchRequest{{Op: "remove", Path: "/spec", Value: nil}},
		},
		{
			name:  "When the request removes a field inside the firmware it should not report a withdrawal",
			patch: domain.PatchRequest{{Op: "remove", Path: DeviceFirmwarePatchPath + "/retryGeneration", Value: nil}},
		},
		{
			name:  "When the request removes the firmware of another resource kind it should not report a withdrawal",
			patch: domain.PatchRequest{{Op: "remove", Path: FleetFirmwarePatchPath, Value: nil}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FirmwareRemovedByPatch(tt.patch, DeviceFirmwarePatchPath))
		})
	}
}

func TestValidatePatchedFirmware(t *testing.T) {
	device := domain.Device{
		ApiVersion: domain.DeviceAPIVersion,
		Kind:       domain.DeviceKind,
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr("device-1")},
		Spec:       &domain.DeviceSpec{Firmware: newTestFirmware(t)},
	}
	withoutFirmware := device
	withoutFirmware.Spec = &domain.DeviceSpec{}

	tests := []struct {
		name    string
		current domain.Device
		patch   domain.PatchRequest
		wantErr []string
	}{
		{
			name:    "When the request does not touch the firmware it should accept it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: "/metadata/labels/site", Value: "lab"}},
		},
		{
			name:    "When the request changes a firmware field it should accept it",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: DeviceFirmwarePatchPath + "/profile", Value: "orin-other-profile"}},
		},
		{
			name:    "When the request adds an updater configuration key it should leave it to the release schema",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: DeviceFirmwarePatchPath + "/updater/config/unknownSetting", Value: true}},
		},
		{
			name:    "When the request adds a field that differs from a firmware field only in case it should reject it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: DeviceFirmwarePatchPath + "/updater/Version", Value: "9.9.9"}},
			wantErr: []string{"spec.firmware.updater", `"Version"`},
		},
		{
			name:    "When the request adds an unknown firmware field it should reject it",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: DeviceFirmwarePatchPath + "/flashNow", Value: true}},
			wantErr: []string{"spec.firmware", `"flashNow"`},
		},
		{
			name:    "When the request adds an unknown field inside a component it should reject it with the component's path",
			current: device,
			patch:   domain.PatchRequest{{Op: "add", Path: DeviceFirmwarePatchPath + "/components/0/flashNow", Value: true}},
			wantErr: []string{"spec.firmware.components[0]", `"flashNow"`},
		},
		{
			name:    "When the request replaces the spec with null it should leave the decision to the caller",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: "/spec", Value: nil}},
		},
		{
			name:    "When the request replaces the spec with firmware carrying an unknown field it should reject it",
			current: withoutFirmware,
			patch: domain.PatchRequest{{Op: "replace", Path: "/spec", Value: map[string]any{
				"firmware": map[string]any{"profile": "p", "Profile": "q", "updater": map[string]any{"name": "u", "version": "1", "config": map[string]any{}}, "components": []any{}, "overlays": []any{}},
			}}},
			wantErr: []string{"spec.firmware", `"Profile"`},
		},
		{
			name:    "When the request removes the firmware it should accept it",
			current: device,
			patch:   domain.PatchRequest{{Op: "remove", Path: DeviceFirmwarePatchPath, Value: nil}},
		},
		{
			name:    "When the request sets the firmware to null it should leave the decision to the caller",
			current: device,
			patch:   domain.PatchRequest{{Op: "replace", Path: DeviceFirmwarePatchPath, Value: nil}},
		},
		{
			name:    "When the patch cannot be applied it should report the error",
			current: withoutFirmware,
			patch:   domain.PatchRequest{{Op: "replace", Path: DeviceFirmwarePatchPath + "/profile", Value: "p"}},
			wantErr: []string{"failed to apply JSON patch"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidatePatchedFirmware(tt.current, tt.patch, DeviceFirmwarePatchPath)
			if len(tt.wantErr) == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			for _, want := range tt.wantErr {
				assert.Contains(t, err.Error(), want)
			}
			assert.NotContains(t, err.Error(), "Schema:")
		})
	}
}
