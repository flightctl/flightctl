package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDeviceIsRebooting(t *testing.T) {
	tests := []struct {
		name    string
		reason  UpdateState
		status  ConditionStatus
		summary DeviceSummaryStatusType
		want    bool
	}{
		{name: "When forward reboot is active it should be rebooting", reason: UpdateStateRebooting, status: ConditionStatusTrue, want: true},
		{name: "When rollback reboot is active it should be rebooting", reason: UpdateStateRollingBack, status: ConditionStatusTrue, summary: DeviceSummaryStatusRebooting, want: true},
		{name: "When ordinary rollback is active it should not be rebooting", reason: UpdateStateRollingBack, status: ConditionStatusTrue, summary: DeviceSummaryStatusOnline},
		{name: "When rollback is terminal it should not be rebooting", reason: UpdateStateRollingBack, status: ConditionStatusFalse, summary: DeviceSummaryStatusRebooting},
		{name: "When other work has a rebooting summary it should not be rebooting", reason: UpdateStateApplyingUpdate, status: ConditionStatusTrue, summary: DeviceSummaryStatusRebooting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &Device{Status: &DeviceStatus{
				Summary: DeviceSummaryStatus{Status: tt.summary},
				Conditions: []Condition{{
					Type: ConditionTypeDeviceUpdating, Status: tt.status, Reason: string(tt.reason),
				}},
			}}
			require.Equal(t, tt.want, device.IsRebooting())
		})
	}

	var nilDevice *Device
	require.False(t, nilDevice.IsRebooting())
	require.False(t, (&Device{}).IsRebooting())
}

func TestDeviceIsConfirmedForwardReboot(t *testing.T) {
	tests := []struct {
		name    string
		version string
		reason  UpdateState
		want    bool
	}{
		{name: "When stable 1.5 agent reports Rebooting it should confirm forward reboot", version: "v1.5.0", reason: UpdateStateRebooting, want: true},
		{name: "When newer stable agent reports Rebooting it should confirm forward reboot", version: "v1.6.1", reason: UpdateStateRebooting, want: true},
		{name: "When older agent reports Rebooting it should leave direction unknown", version: "v1.4.9", reason: UpdateStateRebooting},
		{name: "When prerelease agent reports Rebooting it should leave direction unknown", version: "v1.5.0-rc1", reason: UpdateStateRebooting},
		{name: "When development agent reports Rebooting it should leave direction unknown", version: "v1.5.0-9-gabcdef123", reason: UpdateStateRebooting},
		{name: "When unknown agent reports Rebooting it should leave direction unknown", version: "unknown-dirty", reason: UpdateStateRebooting},
		{name: "When newer agent reports RollingBack it should not confirm forward reboot", version: "v1.5.0", reason: UpdateStateRollingBack},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			device := &Device{Status: &DeviceStatus{
				SystemInfo: DeviceSystemInfo{AgentVersion: tt.version},
				Conditions: []Condition{{Type: ConditionTypeDeviceUpdating, Status: ConditionStatusTrue, Reason: string(tt.reason)}},
			}}
			require.Equal(t, tt.want, device.IsConfirmedForwardReboot())
			require.True(t, device.IsUpdating())
		})
	}

	var nilDevice *Device
	require.False(t, nilDevice.IsConfirmedForwardReboot())
	require.False(t, (&Device{}).IsConfirmedForwardReboot())
}
