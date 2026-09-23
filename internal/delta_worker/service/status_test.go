package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/domain"
	storepkg "github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorePreparingStatus_Fleet(t *testing.T) {
	orgId := uuid.New()
	tv := "tv-1"
	sourceResourceVersion := int64(1)
	fleets := &fakeFleetStatusStore{fleet: &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name:            lo.ToPtr("fleet-1"),
			ResourceVersion: lo.ToPtr("1"),
			Annotations:     &map[string]string{"existing": "value"},
		},
		Status: &domain.FleetStatus{},
	}}
	devices := &fakeDeviceStatusStore{}
	s := NewStorePreparingStatus(fleets, devices)
	prepare := &model.DeltaPrepare{OrgID: orgId, Kind: domain.FleetKind, Name: "fleet-1", TemplateVersion: &tv, SourceResourceVersion: sourceResourceVersion}

	t.Run("When SetPreparing is called it should set FleetDeltaPreparing and deltaGeneration", func(t *testing.T) {
		err := s.SetPreparing(context.Background(), prepare, 1, 3)
		require.NoError(t, err)
		require.NotNil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Equal(t, int64(1), fleets.fleet.Status.DeltaGeneration.Completed)
		assert.Equal(t, int64(3), fleets.fleet.Status.DeltaGeneration.Total)
		cond := domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
		require.NotNil(t, cond)
		assert.Equal(t, domain.ConditionStatusTrue, cond.Status)
		assert.Equal(t, "1/3", cond.Message)
		assert.Equal(t, "1", (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion])
	})

	t.Run("When Clear is called it should omit the condition and deltaGeneration", func(t *testing.T) {
		err := s.Clear(context.Background(), orgId, domain.FleetKind, "fleet-1")
		require.NoError(t, err)
		assert.Nil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
		assert.Empty(t, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion])
	})

	t.Run("When ResumeIfCurrent is called it should clear status for the matching template version", func(t *testing.T) {
		tv := "tv-2"
		fleets.fleet.Metadata.ResourceVersion = lo.ToPtr("2")
		fleets.fleet.Metadata.Annotations = &map[string]string{
			"existing": "value",
		}
		prepare := &model.DeltaPrepare{OrgID: orgId, Kind: domain.FleetKind, Name: "fleet-1", TemplateVersion: &tv, SourceResourceVersion: 2}
		err := s.SetPreparing(context.Background(), prepare, 1, 1)
		require.NoError(t, err)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion:       &tv,
			SourceResourceVersion: 2,
		})
		require.NoError(t, err)
		assert.True(t, result.Matched)
		assert.Nil(t, fleets.fleet.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
		require.NotNil(t, fleets.fleet.Metadata.Annotations)
		assert.Equal(t, tv, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion])
		assert.Equal(t, "value", (*fleets.fleet.Metadata.Annotations)["existing"])
		assert.Empty(t, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion])
		assert.Equal(t, 1, devices.outOfDateCalls)
		assert.Equal(t, util.ResourceOwner(domain.FleetKind, "fleet-1"), devices.outOfDateOwner)

		result, err = s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion:       &tv,
			SourceResourceVersion: 2,
		})
		require.NoError(t, err)
		assert.False(t, result.Matched)
	})

	t.Run("When ResumeIfCurrent identity does not match it should leave the fleet unchanged", func(t *testing.T) {
		currentTV := "tv-current"
		staleTV := "tv-stale"
		fleets.fleet.Metadata.ResourceVersion = lo.ToPtr("3")
		fleets.fleet.Metadata.Annotations = &map[string]string{domain.FleetAnnotationDeltaPrepareResourceVersion: "4"}
		_ = s.SetPreparing(context.Background(), &model.DeltaPrepare{
			OrgID: orgId, Kind: domain.FleetKind, Name: "fleet-1", TemplateVersion: &currentTV, SourceResourceVersion: 4,
		}, 1, 1)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
			TemplateVersion:       &staleTV,
			SourceResourceVersion: 3,
		})
		require.NoError(t, err)
		assert.False(t, result.Matched)
		assert.NotNil(t, domain.FindStatusCondition(fleets.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing))
		assert.Equal(t, "4", (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion])
		assert.Empty(t, (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion])

		t.Run("When a stale source resource version completes it should leave the fleet unchanged", func(t *testing.T) {
			result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", ResumeIdentity{
				TemplateVersion:       &currentTV,
				SourceResourceVersion: 2,
			})
			require.NoError(t, err)
			assert.False(t, result.Matched)
		})
	})

	t.Run("When SetIfCurrent sees a newer source resource version it should leave the fleet unchanged", func(t *testing.T) {
		currentTV := "tv-current"
		fleets.fleet.Metadata.ResourceVersion = lo.ToPtr("11")
		fleets.fleet.Metadata.Annotations = &map[string]string{}
		current := &model.DeltaPrepare{OrgID: orgId, Kind: domain.FleetKind, Name: "fleet-1", TemplateVersion: &currentTV, SourceResourceVersion: 11}
		stale := ResumeIdentity{TemplateVersion: &currentTV, SourceResourceVersion: 10}
		require.NoError(t, s.SetPreparing(context.Background(), current, 2, 3))
		require.NoError(t, s.SetIfCurrent(context.Background(), orgId, domain.FleetKind, "fleet-1", stale, 1, 3))
		assert.Equal(t, int64(2), fleets.fleet.Status.DeltaGeneration.Completed)
		assert.Equal(t, "11", (*fleets.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion])
	})
}

func TestStorePreparingStatus_FleetResumeContinuesWhenOutOfDateUpdateFails(t *testing.T) {
	orgID := uuid.New()
	templateVersion := "tv-1"
	fleet := &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("fleet-1"),
			Annotations: &map[string]string{
				domain.FleetAnnotationDeltaPrepareResourceVersion: "1",
			},
		},
		Status: &domain.FleetStatus{Conditions: []domain.Condition{{
			Type:   domain.ConditionTypeFleetDeltaPreparing,
			Status: domain.ConditionStatusTrue,
		}}},
	}
	devices := &fakeDeviceStatusStore{outOfDateErr: errors.New("device store unavailable")}
	status := NewStorePreparingStatus(&fakeFleetStatusStore{fleet: fleet}, devices)

	result, err := status.ResumeIfCurrent(context.Background(), orgID, domain.FleetKind, "fleet-1", ResumeIdentity{
		TemplateVersion:       &templateVersion,
		SourceResourceVersion: 1,
	})
	require.NoError(t, err)
	assert.True(t, result.Matched)
	assert.Equal(t, templateVersion, (*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion])
	assert.Equal(t, 1, devices.outOfDateCalls)
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
	specHash := "spec-1"
	prepare := &model.DeltaPrepare{OrgID: orgId, Kind: domain.DeviceKind, Name: "d1", SpecHash: &specHash}

	t.Run("When Set is called for a device it should update the preparation status", func(t *testing.T) {
		err := s.SetPreparing(context.Background(), prepare, 0, 1)
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
		_ = s.SetPreparing(context.Background(), prepare, 1, 1)
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{
			SpecHash: &specHash,
		})
		require.NoError(t, err)
		assert.True(t, result.Matched)
		assert.Nil(t, devices.device.Status.DeltaGeneration)
		assert.Nil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))

		result, err = s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &specHash})
		require.NoError(t, err)
		assert.False(t, result.Matched)
	})

	t.Run("When ResumeIfCurrent sees a different device spec hash it should leave status unchanged", func(t *testing.T) {
		_ = s.SetPreparing(context.Background(), prepare, 1, 1)
		staleHash := "spec-stale"
		result, err := s.ResumeIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &staleHash})
		require.NoError(t, err)
		assert.False(t, result.Matched)
		assert.NotNil(t, domain.FindStatusCondition(devices.device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing))
	})

	t.Run("When SetIfCurrent sees a different device spec hash it should leave status unchanged", func(t *testing.T) {
		_ = s.SetPreparing(context.Background(), prepare, 1, 1)
		staleHash := "spec-stale"
		require.NoError(t, s.SetIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &staleHash}, 0, 1))
		assert.Equal(t, int64(1), devices.device.Status.DeltaGeneration.Completed)
	})

	t.Run("When Set is called with an unsupported kind it should return an error", func(t *testing.T) {
		err := s.SetIfCurrent(context.Background(), orgId, "Unknown", "d1", ResumeIdentity{}, 0, 1)
		require.EqualError(t, err, `unsupported preparing status kind "Unknown"`)
	})

	t.Run("When Set is called without a device store it should return an error", func(t *testing.T) {
		err := NewStorePreparingStatus(nil, nil).SetIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &specHash}, 0, 1)
		require.EqualError(t, err, "device store is required")
	})

	t.Run("When the device store returns an error it should propagate it", func(t *testing.T) {
		storeErr := errors.New("device lookup failed")
		err := NewStorePreparingStatus(nil, &fakeDeviceStatusStore{getErr: storeErr}).SetIfCurrent(context.Background(), orgId, domain.DeviceKind, "d1", ResumeIdentity{SpecHash: &specHash}, 0, 1)
		require.ErrorIs(t, err, storeErr)
	})
}

type fakeFleetStatusStore struct {
	fleet *domain.Fleet
}

func (f *fakeFleetStatusStore) ResumeDeltaIfCurrent(_ context.Context, _ uuid.UUID, _ string, templateVersion string, sourceResourceVersion int64) (*domain.Fleet, error) {
	if f.fleet == nil || f.fleet.Metadata.Annotations == nil ||
		(*f.fleet.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion] != fmt.Sprintf("%d", sourceResourceVersion) ||
		domain.FindStatusCondition(f.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing) == nil {
		return nil, nil
	}
	domain.RemoveStatusCondition(&f.fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
	f.fleet.Status.DeltaGeneration = nil
	annotations := *f.fleet.Metadata.Annotations
	annotations[domain.FleetAnnotationTemplateVersion] = templateVersion
	delete(annotations, domain.FleetAnnotationDeltaPrepareResourceVersion)
	*f.fleet.Metadata.Annotations = annotations
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
	device         *domain.Device
	getErr         error
	outOfDateErr   error
	outOfDateCalls int
	outOfDateOwner string
}

func (f *fakeDeviceStatusStore) SetOutOfDate(_ context.Context, _ uuid.UUID, owner string) error {
	f.outOfDateCalls++
	f.outOfDateOwner = owner
	return f.outOfDateErr
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
