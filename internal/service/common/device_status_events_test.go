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
		{name: "When status is unchanged it should skip the notification", change: func(_, _ *domain.Device) {}},
		{name: "When only LastSeen changes it should skip the notification", change: func(_, d *domain.Device) { d.Status.LastSeen = lo.ToPtr(time.Unix(200, 0)) }},
		{name: "When LastSeen is cleared it should skip the notification", change: func(_, d *domain.Device) { d.Status.LastSeen = nil }},
		{name: "When a device is created it should leave reconciliation to ResourceCreated", change: func(_, _ *domain.Device) {}, created: true},
		{name: "When status appears it should notify", change: func(old, _ *domain.Device) { old.Status = nil }, want: 1},
		{name: "When status is cleared it should notify", change: func(_, d *domain.Device) { d.Status = nil }, want: 1},
		{name: "When both statuses are absent it should skip the notification", change: func(old, d *domain.Device) { old.Status, d.Status = nil, nil }},
		{name: "When system information changes it should notify", change: func(_, d *domain.Device) { d.Status.SystemInfo.Architecture = "aarch64" }, want: 1},
		{name: "When progress information changes it should notify", change: func(_, d *domain.Device) { d.Status.Updated.Info = lo.ToPtr("Downloading image") }, want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldStatus, newStatus := domain.NewDeviceStatus(), domain.NewDeviceStatus()
			oldStatus.LastSeen, newStatus.LastSeen = lo.ToPtr(time.Unix(100, 0)), lo.ToPtr(time.Unix(100, 0))
			oldDevice, newDevice := &domain.Device{Status: &oldStatus}, &domain.Device{Status: &newStatus}
			tt.change(oldDevice, newDevice)
			if tt.created {
				oldDevice = nil
			}
			before, err := json.Marshal([]*domain.Device{oldDevice, newDevice})
			require.NoError(t, err)
			updates := ComputeDeviceStatusChanges(context.Background(), oldDevice, newDevice, uuid.New())
			var notifications []ResourceUpdate
			for _, update := range updates {
				if update.Reason == domain.EventReasonDeviceLabelReconciliationRequested {
					notifications = append(notifications, update)
				}
			}
			require.Len(t, notifications, tt.want)
			if tt.want > 0 {
				require.Equal(t, "Device label reconciliation was requested.", notifications[0].Details)
			}
			after, err := json.Marshal([]*domain.Device{oldDevice, newDevice})
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
}
