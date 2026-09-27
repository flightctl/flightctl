package domain

import (
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
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
			require.Equal(t, tt.expected, result)
		})
	}
}
