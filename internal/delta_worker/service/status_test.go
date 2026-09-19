package service

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	storepkg "github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
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

	t.Run("When ResumeIfCurrent is called it should clear status for the matching template version", func(t *testing.T) {
		tv := "tv-2"
		fleets.fleet.Metadata.ResourceVersion = lo.ToPtr("9")
		fleets.fleet.Metadata.Annotations = &map[string]string{
			"existing":                            "value",
			domain.FleetAnnotationTemplateVersion: tv,
		}
		err := s.Set(context.Background(), orgId, domain.FleetKind, "fleet-1", 1, 1)
		require.NoError(t, err)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion: &tv,
		})
		require.NoError(t, err)
		assert.True(t, result.Matched)
		assert.Nil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
		require.NotNil(t, fleets.fleet.Metadata.Annotations)
		assert.Equal(t, tv, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion])

		result, err = s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion: &tv,
		})
		require.NoError(t, err)
		assert.False(t, result.Matched)
	})

	t.Run("When ResumeIfCurrent identity does not match it should leave the fleet unchanged", func(t *testing.T) {
		tv := "tv-stale"
		fleets.fleet.Metadata.Annotations = &map[string]string{}
		_ = s.Set(context.Background(), orgId, domain.FleetKind, "fleet-1", 1, 1)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion: &tv,
		})
		require.NoError(t, err)
		assert.False(t, result.Matched)
		assert.NotNil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
		assert.Empty(t, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion])
	})
}

func TestStorePreparingStatus_Device(t *testing.T) {
	orgId := uuid.New()
	devices := &fakeDeviceStatusStore{device: &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:            lo.ToPtr("d1"),
			ResourceVersion: lo.ToPtr("5"),
			Annotations:     &map[string]string{domain.DeviceAnnotationRenderedSpecHash: "spec-1"},
		},
		Status: &domain.DeviceStatus{},
	}}
	s := NewStorePreparingStatus(nil, devices)

	t.Run("When Set is called for a device it should update the preparation status", func(t *testing.T) {
		err := s.Set(context.Background(), orgId, domain.DeviceKind, "d1", 0, 1)
		require.NoError(t, err)
		cond := domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
		require.NotNil(t, cond)
		assert.Equal(t, "0/1", cond.Message)
		require.NotNil(t, devices.device.Status.DeltaGeneration)
	})

	t.Run("When Clear is called for a device it should omit the preparation status", func(t *testing.T) {
		err := s.Clear(context.Background(), orgId, domain.DeviceKind, "d1")
		require.NoError(t, err)
		assert.Nil(t, devices.device.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))
	})

	t.Run("When ResumeIfCurrent is called it should clear a matching device", func(t *testing.T) {
		hash := "spec-1"
		_ = s.Set(context.Background(), orgId, domain.DeviceKind, "d1", 1, 1)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{
			SpecHash: &hash,
		})
		require.NoError(t, err)
		assert.True(t, result.Matched)
		assert.Nil(t, devices.device.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))

		result, err = s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &hash})
		require.NoError(t, err)
		assert.False(t, result.Matched)
	})

	t.Run("When ResumeIfCurrent sees a different device spec hash it should leave status unchanged", func(t *testing.T) {
		_ = s.Set(context.Background(), orgId, domain.DeviceKind, "d1", 1, 1)
		staleHash := "spec-stale"
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &staleHash})
		require.NoError(t, err)
		assert.False(t, result.Matched)
		assert.NotNil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))
	})

	t.Run("When Set is called with an unsupported kind it should return an error", func(t *testing.T) {
		err := s.Set(context.Background(), orgId, "Unknown", "d1", 0, 1)
		require.EqualError(t, err, `unsupported preparing status kind "Unknown"`)
	})

	t.Run("When Set is called without a device store it should return an error", func(t *testing.T) {
		err := NewStorePreparingStatus(nil, nil).Set(context.Background(), orgId, domain.DeviceKind, "d1", 0, 1)
		require.EqualError(t, err, "device store is required")
	})

	t.Run("When the device store returns an error it should propagate it", func(t *testing.T) {
		storeErr := errors.New("device lookup failed")
		err := NewStorePreparingStatus(nil, &fakeDeviceStatusStore{getErr: storeErr}).Set(context.Background(), orgId, domain.DeviceKind, "d1", 0, 1)
		require.ErrorIs(t, err, storeErr)
	})
}

type fakeFleetStatusStore struct {
	fleet *domain.Fleet
}

func (f *fakeFleetStatusStore) ResumeDeltaIfCurrent(_ context.Context, _ uuid.UUID, _ string, templateVersion string) (*domain.Fleet, error) {
	if f.fleet == nil || f.fleet.Metadata.Annotations == nil || (*f.fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion] != templateVersion ||
		domain.FindStatusCondition(f.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing) == nil {
		return nil, nil
	}
	domain.RemoveStatusCondition(&f.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
	f.fleet.Status.DeltaGeneration = nil
	return f.fleet, nil
}

func (f *fakeFleetStatusStore) Mutate(_ context.Context, _ uuid.UUID, _ string, _ *domain.Fleet, apply fleetstore.FleetApplyFunc) (*domain.Fleet, *domain.Fleet, bool, error) {
	mutation := &fleetstore.FleetMutation{Fleet: f.fleet}
	if err := apply(mutation); err != nil {
		if errors.Is(err, storepkg.ErrMutateSkipWrite) {
			return mutation.Fleet, f.fleet, false, nil
		}
		return nil, f.fleet, false, err
	}
	f.fleet = mutation.Fleet
	return f.fleet, f.fleet, false, nil
}

type fakeDeviceStatusStore struct {
	device *domain.Device
	getErr error
}

func (f *fakeDeviceStatusStore) ResumeDeltaIfCurrent(_ context.Context, _ uuid.UUID, _ string, specHash string) (bool, error) {
	if f.getErr != nil {
		return false, f.getErr
	}
	if f.device == nil || f.device.SpecHash() != specHash || f.device.Status == nil ||
		domain.FindStatusCondition(f.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing) == nil {
		return false, nil
	}
	domain.RemoveStatusCondition(&f.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
	f.device.Status.DeltaGeneration = nil
	return true, nil
}

func (f *fakeDeviceStatusStore) Mutate(_ context.Context, _ uuid.UUID, _ string, _ *domain.Device, apply devicestore.DeviceApplyFunc, _ ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error) {
	if f.getErr != nil {
		return nil, nil, false, f.getErr
	}
	mutation := &devicestore.DeviceMutation{Device: f.device}
	if err := apply(mutation); err != nil {
		if errors.Is(err, storepkg.ErrMutateSkipWrite) {
			return mutation.Device, f.device, false, nil
		}
		return nil, f.device, false, err
	}
	f.device = mutation.Device
	return f.device, f.device, false, nil
}
