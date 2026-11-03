package device

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestEmitDeviceStatusProgressEvents(t *testing.T) {
	for _, tt := range []struct {
		name         string
		change       func(oldDevice, newDevice *domain.Device)
		want         int
		wantReason   domain.EventReason
		wantMetadata bool
	}{
		{
			name: "When only active update progress changes it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Conditions[0].Reason = string(domain.UpdateStateApplyingUpdate)
				d.Status.Conditions[0].Message = "Downloading image"
				d.Status.Conditions[0].LastTransitionTime = time.Unix(200, 0)
				d.Status.Updated.Info = lo.ToPtr("Downloading image")
				d.Status.LastSeen = lo.ToPtr(time.Unix(200, 0))
			},
			want: 1,
		},
		{
			name: "When only active update info changes it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Updated.Info = lo.ToPtr("Downloading image")
			},
			want: 1,
		},
		{
			name: "When another condition changes during an update it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Conditions[1].LastTransitionTime = time.Unix(200, 0)
			},
			want: 1,
		},
		{
			name: "When system info changes during an update it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.SystemInfo.Architecture = "aarch64"
			},
			want: 1,
		},
		{
			name: "When applications change during an update it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Applications = []domain.DeviceApplicationStatus{{Name: "workload"}}
			},
			want: 1,
		},
		{
			name: "When application summary changes during an update it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusError
			},
			want: 1,
		},
		{
			name: "When systemd changes during an update it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Systemd = &[]domain.SystemdUnitStatus{{}}
			},
			want: 1,
		},
		{
			name: "When the update summary status changes it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
			},
			want:       1,
			wantReason: domain.EventReasonDeviceContentUpToDate,
		},
		{
			name: "When Updating is added it should reconcile",
			change: func(old, _ *domain.Device) {
				old.Status.Conditions = old.Status.Conditions[1:]
			},
			want: 1,
		},
		{
			name: "When Updating is removed it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Conditions = d.Status.Conditions[1:]
			},
			want: 1,
		},
		{
			name: "When an update starts from a terminal state it should reconcile",
			change: func(old, _ *domain.Device) {
				old.Status.Conditions[0].Status = domain.ConditionStatusFalse
				old.Status.Conditions[0].Reason = string(domain.UpdateStateUpdated)
			},
			want: 1,
		},
		{
			name: "When Updating becomes unknown it should reconcile",
			change: func(_, d *domain.Device) {
				d.Status.Conditions[0].Status = domain.ConditionStatusUnknown
			},
			want: 1,
		},
		{
			name: "When info changes with no Updating condition it should reconcile",
			change: func(old, d *domain.Device) {
				old.Status.Conditions = old.Status.Conditions[1:]
				d.Status.Conditions = d.Status.Conditions[1:]
				d.Status.Updated.Info = lo.ToPtr("New info")
			},
			want: 1,
		},
		{
			name: "When metadata changes alongside progress it should keep the metadata event",
			change: func(_, d *domain.Device) {
				d.Metadata.Labels = &map[string]string{"site": "east"}
			},
			want:         1,
			wantMetadata: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			oldDevice, newDevice := prepareTestDeviceForEvents("dev1"), prepareTestDeviceForEvents("dev1")
			for _, d := range []*domain.Device{oldDevice, newDevice} {
				d.Status.Updated.Status = domain.DeviceUpdatedStatusUpdating
				d.Status.Conditions = []domain.Condition{
					{Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue, Reason: string(domain.UpdateStatePreparing), LastTransitionTime: time.Unix(100, 0)},
					{Type: "LabelsSynced", Status: domain.ConditionStatusTrue, LastTransitionTime: time.Unix(100, 0)},
				}
			}
			newDevice.Status.Conditions[0].Message = "Update progressing"
			newDevice.Status.Updated.Info = lo.ToPtr("Update progressing")
			tt.change(oldDevice, newDevice)
			oldStatus, newStatus := *oldDevice.Status, *newDevice.Status
			oldStatus.Conditions = slices.Clone(oldStatus.Conditions)
			newStatus.Conditions = slices.Clone(newStatus.Conditions)
			ev := &fakeEvents{}
			EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", oldDevice, newDevice, false, nil)
			var count int
			for _, event := range ev.created {
				if isStatusUpdatedEvent(event) {
					count++
				}
			}
			require.Equal(t, tt.want, count)
			require.Equal(t, oldStatus, *oldDevice.Status)
			require.Equal(t, newStatus, *newDevice.Status)
			if tt.wantMetadata {
				require.Len(t, ev.created, 2)
				require.Equal(t, domain.EventReasonResourceUpdated, ev.created[1].Reason)
				require.NotNil(t, ev.created[1].Details)
			}
			if tt.wantReason != "" {
				require.True(t, slices.ContainsFunc(ev.created, func(e *domain.Event) bool { return e.Reason == tt.wantReason }))
			}
		})
	}

	for _, reason := range []domain.UpdateState{domain.UpdateStateUpdated, domain.UpdateStateError, domain.UpdateStateCanceled} {
		t.Run("When Updating becomes "+string(reason)+" it should reconcile", func(t *testing.T) {
			oldStatus, newStatus := domain.NewDeviceStatus(), domain.NewDeviceStatus()
			oldStatus.Conditions = []domain.Condition{{Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue, Reason: string(domain.UpdateStateApplyingUpdate)}}
			newStatus.Conditions = []domain.Condition{{Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusFalse, Reason: string(reason)}}
			assertStatusUpdate := func() {
				updates := common.ComputeDeviceStatusChanges(context.Background(), &domain.Device{Status: &oldStatus}, &domain.Device{Status: &newStatus}, uuid.New())
				require.True(t, slices.ContainsFunc(updates, func(update common.ResourceUpdate) bool {
					return update.Reason == domain.EventReasonDeviceLabelReconciliationRequested
				}))
			}
			assertStatusUpdate()
			oldStatus = newStatus
			newStatus.Conditions = slices.Clone(oldStatus.Conditions)
			newStatus.Conditions[0].Message = "Terminal detail"
			assertStatusUpdate()
			newStatus = oldStatus
			newStatus.Updated.Info = lo.ToPtr("Terminal info")
			assertStatusUpdate()
		})
	}
}
