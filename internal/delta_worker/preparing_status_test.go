package delta_worker

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestStorePreparingStatus_Fleet(t *testing.T) {
	orgId := uuid.New()
	fleet := &domain.Fleet{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet-1")},
		Status:   &domain.FleetStatus{},
	}
	ctrl := gomock.NewController(t)
	fleets := fleetservice.NewMockService(ctrl)
	fleets.EXPECT().GetFleetStatus(gomock.Any(), orgId, "fleet-1").Return(fleet, domain.StatusOK()).Times(2)
	fleets.EXPECT().ReplaceFleetStatus(gomock.Any(), orgId, "fleet-1", gomock.Any()).DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, updated domain.Fleet) (*domain.Fleet, domain.Status) {
		*fleet = updated
		return fleet, domain.StatusOK()
	}).Times(2)
	s := NewServicePreparingStatus(fleets, nil)

	t.Run("When Set is called it should set FleetDeltaPreparing and deltaGeneration", func(t *testing.T) {
		err := s.Set(context.Background(), orgId, domain.FleetKind, "fleet-1", 1, 3)
		require.NoError(t, err)
		require.NotNil(t, fleet.Status.DeltaGeneration)
		assert.Equal(t, int64(1), fleet.Status.DeltaGeneration.Completed)
		assert.Equal(t, int64(3), fleet.Status.DeltaGeneration.Total)
		cond := domain.FindStatusCondition(fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
		require.NotNil(t, cond)
		assert.Equal(t, domain.ConditionStatusTrue, cond.Status)
		assert.Equal(t, "1/3", cond.Message)
	})

	t.Run("When Clear is called it should omit the condition and deltaGeneration", func(t *testing.T) {
		err := s.Clear(context.Background(), orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.Nil(t, fleet.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
	})
}

func TestStorePreparingStatus_Device(t *testing.T) {
	orgId := uuid.New()
	device := &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("d1")},
		Status:   &domain.DeviceStatus{},
	}
	ctrl := gomock.NewController(t)
	devices := deviceservice.NewMockService(ctrl)
	devices.EXPECT().GetDevice(gomock.Any(), orgId, "d1").Return(device, domain.StatusOK()).Times(2)
	devices.EXPECT().ReplaceDeviceStatus(gomock.Any(), orgId, "d1", gomock.Any(), false).DoAndReturn(func(_ context.Context, _ uuid.UUID, _ string, updated domain.Device, _ bool) (*domain.Device, domain.Status) {
		*device = updated
		return device, domain.StatusOK()
	}).Times(2)
	s := NewServicePreparingStatus(nil, devices)

	err := s.Set(context.Background(), orgId, domain.DeviceKind, "d1", 0, 1)
	require.NoError(t, err)
	cond := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
	require.NotNil(t, cond)
	assert.Equal(t, "0/1", cond.Message)
	require.NotNil(t, device.Status.DeltaGeneration)

	err = s.Clear(context.Background(), orgId, domain.DeviceKind, "d1")
	require.NoError(t, err)
	assert.Nil(t, device.Status.DeltaGeneration)
	assert.Nil(t, domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))
}
