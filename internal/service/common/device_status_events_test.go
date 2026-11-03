package common

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestComputeDeviceStatusChanges_LabelReconciliationRequest(t *testing.T) {
	for _, tt := range []struct {
		name    string
		change  func(oldDevice, newDevice *domain.Device)
		created bool
		want    int
	}{
		{name: "When status is unchanged it should skip the request", change: func(_, _ *domain.Device) {}},
		{name: "When only LastSeen changes it should skip the request", change: func(_, d *domain.Device) { d.Status.LastSeen = lo.ToPtr(time.Unix(200, 0)) }},
		{name: "When LastSeen is cleared it should skip the request", change: func(_, d *domain.Device) { d.Status.LastSeen = nil }},
		{name: "When a device is created it should leave reconciliation to ResourceCreated", change: func(_, _ *domain.Device) {}, created: true},
		{name: "When status appears it should request reconciliation", change: func(old, _ *domain.Device) { old.Status = nil }, want: 1},
		{name: "When status is cleared it should request reconciliation", change: func(_, d *domain.Device) { d.Status = nil }, want: 1},
		{name: "When both statuses are absent it should skip the request", change: func(old, d *domain.Device) { old.Status, d.Status = nil, nil }},
		{name: "When system information changes it should request reconciliation", change: func(_, d *domain.Device) { d.Status.SystemInfo.Architecture = "aarch64" }, want: 1},
		{name: "When LastSeen and system information change it should request reconciliation", change: func(_, d *domain.Device) {
			d.Status.LastSeen = lo.ToPtr(time.Unix(200, 0))
			d.Status.SystemInfo.Architecture = "aarch64"
		}, want: 1},
		{name: "When progress information changes it should request reconciliation", change: func(_, d *domain.Device) { d.Status.Updated.Info = lo.ToPtr("Downloading image") }, want: 1},
		{name: "When a condition message changes it should request reconciliation", change: func(_, d *domain.Device) { d.Status.Conditions[0].Message = "Downloading image" }, want: 1},
		{name: "When a condition timestamp changes it should request reconciliation", change: func(_, d *domain.Device) { d.Status.Conditions[0].LastTransitionTime = time.Unix(200, 0) }, want: 1},
		{name: "When a condition is removed it should request reconciliation", change: func(_, d *domain.Device) { d.Status.Conditions = nil }, want: 1},
		{name: "When a condition becomes terminal it should request reconciliation", change: func(_, d *domain.Device) {
			d.Status.Conditions[0].Status = domain.ConditionStatusFalse
			d.Status.Conditions[0].Reason = string(domain.UpdateStateUpdated)
		}, want: 1},
		{name: "When applications change it should request reconciliation", change: func(_, d *domain.Device) {
			d.Status.Applications = []domain.DeviceApplicationStatus{{Name: "workload"}}
		}, want: 1},
		{name: "When application summary changes it should request reconciliation", change: func(_, d *domain.Device) { d.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusError }, want: 1},
		{name: "When systemd units change it should request reconciliation", change: func(_, d *domain.Device) { d.Status.Systemd = &[]domain.SystemdUnitStatus{{Unit: "sshd.service"}} }, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldStatus, newStatus := domain.NewDeviceStatus(), domain.NewDeviceStatus()
			for _, status := range []*domain.DeviceStatus{&oldStatus, &newStatus} {
				status.LastSeen = lo.ToPtr(time.Unix(100, 0))
				status.Updated.Status = domain.DeviceUpdatedStatusUpdating
				status.Conditions = []domain.Condition{{
					Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue,
					Reason: string(domain.UpdateStateApplyingUpdate), LastTransitionTime: time.Unix(100, 0),
				}}
			}
			oldDevice, newDevice := &domain.Device{Status: &oldStatus}, &domain.Device{Status: &newStatus}
			tt.change(oldDevice, newDevice)
			if tt.created {
				oldDevice = nil
			}
			oldLastSeen, newLastSeen := oldStatus.LastSeen, newStatus.LastSeen
			before, err := json.Marshal([]*domain.Device{oldDevice, newDevice})
			require.NoError(t, err)
			updates := ComputeDeviceStatusChanges(context.Background(), oldDevice, newDevice, uuid.New())
			var requests []ResourceUpdate
			for _, update := range updates {
				if update.Reason == domain.EventReasonDeviceLabelReconciliationRequested {
					requests = append(requests, update)
				}
			}
			require.Len(t, requests, tt.want)
			if tt.want > 0 {
				require.Equal(t, "Device label reconciliation was requested.", requests[0].Details)
			}
			after, err := json.Marshal([]*domain.Device{oldDevice, newDevice})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
			require.Equal(t, oldLastSeen, oldStatus.LastSeen)
			require.Equal(t, newLastSeen, newStatus.LastSeen)
		})
	}
}
