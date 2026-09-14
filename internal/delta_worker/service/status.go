package service

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	storepkg "github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
)

type fleetStatusStore interface {
	Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Fleet, apply fleetstore.FleetApplyFunc) (*domain.Fleet, *domain.Fleet, bool, error)
}

type PreparingStatus interface {
	Set(ctx context.Context, orgID uuid.UUID, kind, name string, completed, total int) error
	Clear(ctx context.Context, orgID uuid.UUID, kind, name string) error
}

type deviceStatusStore interface {
	Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Device, apply devicestore.DeviceApplyFunc, opts ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error)
}

type storePreparingStatus struct {
	fleets  fleetStatusStore
	devices deviceStatusStore
}

func NewStorePreparingStatus(fleets fleetStatusStore, devices deviceStatusStore) PreparingStatus {
	return &storePreparingStatus{fleets: fleets, devices: devices}
}

func (s *storePreparingStatus) Set(ctx context.Context, orgId uuid.UUID, kind, name string, completed, total int) error {
	switch kind {
	case domain.FleetKind:
		return s.setFleet(ctx, orgId, name, completed, total)
	case domain.DeviceKind:
		return s.setDevice(ctx, orgId, name, completed, total)
	default:
		return fmt.Errorf("unsupported preparing status kind %q", kind)
	}
}

func (s *storePreparingStatus) Clear(ctx context.Context, orgId uuid.UUID, kind, name string) error {
	switch kind {
	case domain.FleetKind:
		return s.clearFleet(ctx, orgId, name)
	case domain.DeviceKind:
		return s.clearDevice(ctx, orgId, name)
	default:
		return fmt.Errorf("unsupported preparing status kind %q", kind)
	}
}

func (s *storePreparingStatus) setFleet(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
	if s.fleets == nil {
		return fmt.Errorf("fleet store is required")
	}
	condition := preparingCondition(domain.ConditionTypeFleetDeltaPreparing, completed, total)
	generation := newDeltaGenerationStatus(completed, total)
	_, _, _, err := s.fleets.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if m.Fleet.Status == nil {
			m.Fleet.Status = &domain.FleetStatus{}
		}
		domain.SetStatusCondition(&m.Fleet.Status.Conditions, condition)
		m.Fleet.Status.DeltaGeneration = generation
		return nil
	})
	return err
}

func (s *storePreparingStatus) clearFleet(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.fleets == nil {
		return fmt.Errorf("fleet store is required")
	}
	_, _, _, err := s.fleets.Mutate(ctx, orgId, name, nil, func(m *fleetstore.FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if m.Fleet.Status == nil {
			return storepkg.ErrMutateSkipWrite
		}
		domain.RemoveStatusCondition(&m.Fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
		m.Fleet.Status.DeltaGeneration = nil
		return nil
	})
	return err
}

func (s *storePreparingStatus) setDevice(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	condition := preparingCondition(domain.ConditionTypeDeviceDeltaPreparing, completed, total)
	generation := newDeltaGenerationStatus(completed, total)
	_, _, _, err := s.devices.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if m.Device.Status == nil {
			m.Device.Status = &domain.DeviceStatus{}
		}
		domain.SetStatusCondition(&m.Device.Status.Conditions, condition)
		m.Device.Status.DeltaGeneration = generation
		return nil
	})
	return err
}

func (s *storePreparingStatus) clearDevice(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	_, _, _, err := s.devices.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if m.Device.Status == nil {
			return storepkg.ErrMutateSkipWrite
		}
		domain.RemoveStatusCondition(&m.Device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
		m.Device.Status.DeltaGeneration = nil
		return nil
	})
	return err
}

func newDeltaGenerationStatus(completed, total int) *domain.DeltaGenerationStatus {
	now := time.Now().UTC()
	return &domain.DeltaGenerationStatus{
		Completed:   int64(completed),
		Total:       int64(total),
		LastUpdated: &now,
	}
}

func preparingCondition(condType domain.ConditionType, completed, total int) domain.Condition {
	return domain.Condition{
		Type:    condType,
		Status:  domain.ConditionStatusTrue,
		Reason:  string(condType),
		Message: fmt.Sprintf("%d/%d", completed, total),
	}
}
