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

func TestFirmwareAttemptOutstanding(t *testing.T) {
	tests := []struct {
		name    string
		attempt *FirmwareAttemptStatus
		want    bool
	}{
		{name: "When there is no attempt it should not be outstanding", attempt: nil, want: false},
		{name: "When the attempt is Preparing it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhasePreparing}, want: true},
		{name: "When the attempt is WaitingForPolicy it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseWaitingForPolicy}, want: true},
		{name: "When the attempt is Staging it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseStaging}, want: true},
		{name: "When the attempt is AwaitingReboot it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseAwaitingReboot}, want: true},
		{name: "When the attempt is Verifying it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseVerifying}, want: true},
		{name: "When the attempt Succeeded it should not be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseSucceeded}, want: false},
		{name: "When the attempt Failed it should not be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseFailed}, want: false},
		{name: "When the attempt was Canceled it should not be outstanding", attempt: &FirmwareAttemptStatus{Phase: FirmwareAttemptPhaseCanceled}, want: false},
		{name: "When the attempt phase is not recognized it should be outstanding", attempt: &FirmwareAttemptStatus{Phase: "Flashing"}, want: true},
		{name: "When the attempt phase is empty it should be outstanding", attempt: &FirmwareAttemptStatus{}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FirmwareAttemptOutstanding(tt.attempt))
		})
	}
}

func TestDeviceRequiresFirmwareHold(t *testing.T) {
	desired := &FirmwareSpec{Profile: "orin-example-profile"}
	outstanding := &FirmwareAttemptStatus{Id: "attempt-1", Phase: FirmwareAttemptPhaseStaging}
	terminal := &FirmwareAttemptStatus{Id: "attempt-1", Phase: FirmwareAttemptPhaseSucceeded}
	withFirmware := func(firmware *FirmwareSpec, status *FirmwareStatus) *Device {
		return &Device{
			Spec:   &DeviceSpec{Firmware: firmware},
			Status: &DeviceStatus{Firmware: status},
		}
	}

	tests := []struct {
		name   string
		device *Device
		want   bool
	}{
		{name: "When the device is nil it should not hold", device: nil, want: false},
		{name: "When the device has no spec and no status it should not hold", device: &Device{}, want: false},
		{name: "When firmware is absent and no firmware status is reported it should not hold", device: withFirmware(nil, nil), want: false},
		{name: "When firmware is absent and the status has no attempt it should not hold", device: withFirmware(nil, &FirmwareStatus{}), want: false},
		{name: "When firmware is absent and only terminal attempt history remains it should not hold", device: withFirmware(nil, &FirmwareStatus{Attempt: terminal}), want: false},
		{name: "When firmware was withdrawn while an attempt is outstanding it should hold", device: withFirmware(nil, &FirmwareStatus{Attempt: outstanding}), want: true},
		{name: "When desired firmware is present and no firmware status is reported it should hold", device: withFirmware(desired, nil), want: true},
		{name: "When desired firmware is present and an attempt is outstanding it should hold", device: withFirmware(desired, &FirmwareStatus{Attempt: outstanding}), want: true},
		{name: "When desired firmware is present and the attempt is terminal it should hold", device: withFirmware(desired, &FirmwareStatus{Attempt: terminal}), want: true},
		{name: "When desired firmware is present and the device has no status it should hold", device: &Device{Spec: &DeviceSpec{Firmware: desired}}, want: true},
		{name: "When an outstanding attempt is reported for a device without a spec it should hold", device: &Device{Status: &DeviceStatus{Firmware: &FirmwareStatus{Attempt: outstanding}}}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeviceRequiresFirmwareHold(tt.device))
		})
	}
}
