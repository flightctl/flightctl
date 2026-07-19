package device

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func prepareTestDeviceForEvents(name string) *domain.Device {
	status := domain.NewDeviceStatus()
	return &domain.Device{
		ApiVersion: "v1beta1",
		Kind:       "Device",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name), Labels: &map[string]string{"labelKey": "labelValue"}},
		Spec:       &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "img"}},
		Status:     &status,
	}
}

func TestEmitDeviceUpdatedEvent(t *testing.T) {
	t.Run("When err is non-nil it should emit a creation/update failure event", func(t *testing.T) {
		ev := &fakeEvents{}
		EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", nil, nil, true, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreationFailed, ev.created[0].Reason)
	})

	t.Run("When created is true it should emit a single resource-created event", func(t *testing.T) {
		ev := &fakeEvents{}
		device := prepareTestDeviceForEvents("dev1")
		EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", nil, device, true, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonResourceCreated, ev.created[0].Reason)
	})

	t.Run("When updated with an empty old device it should not panic and should emit a status event", func(t *testing.T) {
		ev := &fakeEvents{}
		device := prepareTestDeviceForEvents("dev1")
		device.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
		oldDevice := &domain.Device{}
		require.NotPanics(t, func() {
			EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", oldDevice, device, false, nil)
		})
		require.Len(t, ev.created, 1)
	})

	t.Run("When multiple status fields transition to Unknown it should deduplicate DeviceDisconnected events", func(t *testing.T) {
		ev := &fakeEvents{}
		oldDevice := prepareTestDeviceForEvents("dev1")
		oldDevice.Status.Summary.Status = domain.DeviceSummaryStatusOnline
		oldDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUpToDate
		oldDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusHealthy

		newDevice := prepareTestDeviceForEvents("dev1")
		newDevice.Status.Summary.Status = domain.DeviceSummaryStatusUnknown
		newDevice.Status.Updated.Status = domain.DeviceUpdatedStatusUnknown
		newDevice.Status.ApplicationsSummary.Status = domain.ApplicationsSummaryStatusUnknown

		EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", oldDevice, newDevice, false, nil)

		count := 0
		for _, e := range ev.created {
			if e.Reason == domain.EventReasonDeviceDisconnected {
				count++
			}
		}
		require.Equal(t, 1, count)
	})

	t.Run("When the resources cannot be cast to *domain.Device it should no-op", func(t *testing.T) {
		ev := &fakeEvents{}
		EmitDeviceUpdatedEvent(context.Background(), ev, logrus.New(), domain.DeviceKind, uuid.New(), "dev1", "not-a-device", "also-not", false, nil)
		require.Empty(t, ev.created)
	})
}

func TestEmitDeviceDecommissionEvent(t *testing.T) {
	t.Run("When err is nil it should emit a decommission-success event", func(t *testing.T) {
		ev := &fakeEvents{}
		EmitDeviceDecommissionEvent(context.Background(), ev, domain.DeviceKind, uuid.New(), "dev1", false, nil)
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonDeviceDecommissioned, ev.created[0].Reason)
	})

	t.Run("When err is non-nil it should emit a decommission-failure event", func(t *testing.T) {
		ev := &fakeEvents{}
		EmitDeviceDecommissionEvent(context.Background(), ev, domain.DeviceKind, uuid.New(), "dev1", false, errors.New("boom"))
		require.Len(t, ev.created, 1)
		require.Equal(t, domain.EventReasonDeviceDecommissionFailed, ev.created[0].Reason)
	})
}
