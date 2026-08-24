package delta_worker

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePreparingStatus_Fleet(t *testing.T) {
	orgId := uuid.New()
	fleets := &fakeFleetStatusStore{fleet: &domain.Fleet{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("fleet-1")},
		Status:   &domain.FleetStatus{},
	}}
	s := NewStorePreparingStatus(fleets, nil)

	t.Run("When Set is called it should set FleetDeltaPreparing and deltaGeneration", func(t *testing.T) {
		err := s.Set(context.Background(), orgId, domain.FleetKind, "fleet-1", 1, 3)
		require.NoError(t, err)
		require.NotNil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Equal(t, int64(1), fleets.fleet.Status.DeltaGeneration.Completed)
		assert.Equal(t, int64(3), fleets.fleet.Status.DeltaGeneration.Total)
		cond := domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
		require.NotNil(t, cond)
		assert.Equal(t, domain.ConditionStatusTrue, cond.Status)
		assert.Equal(t, "1/3", cond.Message)
	})

	t.Run("When Clear is called it should omit the condition and deltaGeneration", func(t *testing.T) {
		err := s.Clear(context.Background(), orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.Nil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
	})
}

func TestStorePreparingStatus_Device(t *testing.T) {
	orgId := uuid.New()
	devices := &fakeDeviceStatusStore{device: &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("d1")},
		Status:   &domain.DeviceStatus{},
	}}
	s := NewStorePreparingStatus(nil, devices)

	err := s.Set(context.Background(), orgId, domain.DeviceKind, "d1", 0, 1)
	require.NoError(t, err)
	cond := domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
	require.NotNil(t, cond)
	assert.Equal(t, "0/1", cond.Message)
	require.NotNil(t, devices.device.Status.DeltaGeneration)

	err = s.Clear(context.Background(), orgId, domain.DeviceKind, "d1")
	require.NoError(t, err)
	assert.Nil(t, devices.device.Status.DeltaGeneration)
	assert.Nil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))
}

type fakeFleetStatusStore struct {
	fleet *domain.Fleet
}

func (f *fakeFleetStatusStore) Get(_ context.Context, _ uuid.UUID, _ string, _ ...fleetstore.GetOption) (*domain.Fleet, error) {
	return f.fleet, nil
}

func (f *fakeFleetStatusStore) UpdateStatus(_ context.Context, _ uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, *domain.Fleet, error) {
	f.fleet = fleet
	return fleet, fleet, nil
}

type fakeDeviceStatusStore struct {
	device *domain.Device
}

func (f *fakeDeviceStatusStore) Get(_ context.Context, _ uuid.UUID, _ string) (*domain.Device, error) {
	return f.device, nil
}

func (f *fakeDeviceStatusStore) UpdateStatus(_ context.Context, _ uuid.UUID, device *domain.Device, _ *domain.Device) (*domain.Device, *domain.Device, error) {
	f.device = device
	return device, device, nil
}
