package device

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestDecideAwaitingReconnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		annotations           map[string]string
		deviceReportedVersion *string
		want                  awaitingReconnectDecision
	}{
		{
			name:        "When the device has no awaiting reconnect annotation it should not apply",
			annotations: map[string]string{},
			want:        awaitingReconnectDecision{Apply: false},
		},
		{
			name: "When awaiting reconnect annotation is false it should not apply",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "false",
			},
			want: awaitingReconnectDecision{Apply: false},
		},
		{
			name: "When device version is less than or equal to service version it should clear awaiting reconnect as online",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "5",
			},
			deviceReportedVersion: lo.ToPtr("3"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     false,
				SetConflictPaused:     false,
				SummaryStatus:         string(domain.DeviceSummaryStatusOnline),
				SummaryInfo:           "Device is up to date",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "3",
			},
		},
		{
			name: "When device version equals service version it should set updated status up to date",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "5",
			},
			deviceReportedVersion: lo.ToPtr("5"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     false,
				SetConflictPaused:     false,
				SummaryStatus:         string(domain.DeviceSummaryStatusOnline),
				SummaryInfo:           "Device is up to date",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusUpToDate),
				ConfigRenderedVersion: "5",
			},
		},
		{
			name: "When device version is greater than service version it should set conflict paused",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "3",
			},
			deviceReportedVersion: lo.ToPtr("5"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     true,
				SetConflictPaused:     true,
				SummaryStatus:         string(domain.DeviceSummaryStatusConflictPaused),
				SummaryInfo:           "Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required. (device reported version 5 > device version known to service 3)",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "5",
			},
		},
		{
			name: "When device reported version is nil it should treat device version as 0",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "5",
			},
			deviceReportedVersion: nil,
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     false,
				SetConflictPaused:     false,
				SummaryStatus:         string(domain.DeviceSummaryStatusOnline),
				SummaryInfo:           "Device is up to date",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "0",
			},
		},
		{
			name: "When device reported version is empty it should treat device version as 0",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "5",
			},
			deviceReportedVersion: lo.ToPtr(""),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     false,
				SetConflictPaused:     false,
				SummaryStatus:         string(domain.DeviceSummaryStatusOnline),
				SummaryInfo:           "Device is up to date",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "0",
			},
		},
		{
			name: "When device reported version is unparseable it should compare as 0 and persist the raw string",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "5",
			},
			deviceReportedVersion: lo.ToPtr("invalid"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     false,
				SetConflictPaused:     false,
				SummaryStatus:         string(domain.DeviceSummaryStatusOnline),
				SummaryInfo:           "Device is up to date",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "invalid",
			},
		},
		{
			name: "When service version is unparseable it should compare as 0",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "invalid",
			},
			deviceReportedVersion: lo.ToPtr("1"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     true,
				SetConflictPaused:     true,
				SummaryStatus:         string(domain.DeviceSummaryStatusConflictPaused),
				SummaryInfo:           "Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required. (device reported version 1 > device version known to service 0)",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "1",
			},
		},
		{
			name: "When service version annotation is missing it should compare as 0",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
			},
			deviceReportedVersion: lo.ToPtr("1"),
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     true,
				SetConflictPaused:     true,
				SummaryStatus:         string(domain.DeviceSummaryStatusConflictPaused),
				SummaryInfo:           "Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required. (device reported version 1 > device version known to service 0)",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "1",
			},
		},
		{
			name: "When conflict paused and reported version is nil it should display unknown in the info message",
			annotations: map[string]string{
				domain.DeviceAnnotationAwaitingReconnect: "true",
				domain.DeviceAnnotationRenderedVersion:   "-1",
			},
			deviceReportedVersion: nil,
			want: awaitingReconnectDecision{
				Apply:                 true,
				WasConflictPaused:     true,
				SetConflictPaused:     true,
				SummaryStatus:         string(domain.DeviceSummaryStatusConflictPaused),
				SummaryInfo:           "Device reconciliation is paused due to a state conflict between the service and the device's agent; manual intervention is required. (device reported version unknown > device version known to service -1)",
				UpdatedStatus:         string(domain.DeviceUpdatedStatusOutOfDate),
				ConfigRenderedVersion: "0",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			device := &domain.Device{
				Metadata: domain.ObjectMeta{
					Annotations: lo.ToPtr(tt.annotations),
				},
			}
			got := decideAwaitingReconnect(device, tt.deviceReportedVersion)
			require.Equal(t, tt.want, got)
		})
	}
}
