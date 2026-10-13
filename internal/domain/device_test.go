package domain

import (
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsDeviceEnrollmentHooksGated(t *testing.T) {
	tests := []struct {
		name     string
		device   *Device
		expected bool
	}{
		{
			name:     "When device is nil it should not be gated",
			device:   nil,
			expected: false,
		},
		{
			name:     "When device status is nil it should not be gated",
			device:   &Device{Status: nil},
			expected: false,
		},
		{
			name: "When device has no conditions it should not be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{},
				},
			},
			expected: false,
		},
		{
			name: "When device has no EnrollmentHooks condition it should not be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceUpdating, Status: ConditionStatusTrue},
					},
				},
			},
			expected: false,
		},
		{
			name: "When EnrollmentHooks is False/NotifyPending it should be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusFalse, Reason: v1beta1.EnrollmentHooksReasonNotifyPending},
					},
				},
			},
			expected: true,
		},
		{
			name: "When EnrollmentHooks is False/Pending it should be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusFalse, Reason: v1beta1.EnrollmentHooksReasonPending},
					},
				},
			},
			expected: true,
		},
		{
			name: "When EnrollmentHooks is False/Failed it should be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusFalse, Reason: v1beta1.EnrollmentHooksReasonFailed},
					},
				},
			},
			expected: true,
		},
		{
			name: "When EnrollmentHooks is True/Succeeded it should not be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusTrue, Reason: v1beta1.EnrollmentHooksReasonSucceeded},
					},
				},
			},
			expected: false,
		},
		{
			name: "When EnrollmentHooks is True/Continued it should not be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusTrue, Reason: v1beta1.EnrollmentHooksReasonContinued},
					},
				},
			},
			expected: false,
		},
		{
			name: "When EnrollmentHooks is True/ManualOverride it should not be gated",
			device: &Device{
				Status: &DeviceStatus{
					Conditions: []Condition{
						{Type: ConditionTypeDeviceEnrollmentHooks, Status: ConditionStatusTrue, Reason: v1beta1.EnrollmentHooksReasonManualOverride},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsDeviceEnrollmentHooksGated(tt.device)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestDeviceStatusOsMode(t *testing.T) {
	image := OsModeImage
	packageMode := OsModePackage
	empty := OsModeType("")

	tests := []struct {
		name   string
		status *DeviceStatus
		want   *OsModeType
	}{
		{
			name:   "When status is nil it should return nil",
			status: nil,
			want:   nil,
		},
		{
			name:   "When neither systemInfo nor capabilities report a mode it should return nil",
			status: &DeviceStatus{},
			want:   nil,
		},
		{
			name: "When systemInfo reports a mode it should be preferred over capabilities",
			status: &DeviceStatus{
				SystemInfo:   DeviceSystemInfo{OsMode: &image},
				Capabilities: &DeviceCapabilities{OsMode: &packageMode},
			},
			want: &image,
		},
		{
			name: "When systemInfo osMode is empty it should fall back to capabilities",
			status: &DeviceStatus{
				SystemInfo:   DeviceSystemInfo{OsMode: &empty},
				Capabilities: &DeviceCapabilities{OsMode: &packageMode},
			},
			want: &packageMode,
		},
		{
			name: "When systemInfo osMode is unset it should fall back to capabilities",
			status: &DeviceStatus{
				Capabilities: &DeviceCapabilities{OsMode: &image},
			},
			want: &image,
		},
		{
			name: "When only systemInfo reports a mode it should return it",
			status: &DeviceStatus{
				SystemInfo: DeviceSystemInfo{OsMode: &packageMode},
			},
			want: &packageMode,
		},
		{
			name: "When capabilities is present but osMode is unset it should return nil",
			status: &DeviceStatus{
				Capabilities: &DeviceCapabilities{},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeviceStatusOsMode(tt.status)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tt.want, *got)
		})
	}
}
