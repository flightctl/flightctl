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
	ResumeDeltaIfCurrent(ctx context.Context, orgID uuid.UUID, name, templateVersion string) (*domain.Fleet, error)
}

// ResumeIdentity identifies the resource state for which a delta prepare was
// created. The resource stores use these values directly in their conditional
// UPDATE predicates.
type ResumeIdentity struct {
	TemplateVersion *string
	SpecHash        *string
}

// ResumeResult reports whether the conditional resource update matched and was
// applied. A non-matching result means the caller must stop without emitting a
// completion event.
type ResumeResult struct {
	Matched bool
	Fleet   *domain.Fleet
}

type deviceStatusStore interface {
	Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Device, apply devicestore.DeviceApplyFunc, opts ...devicestore.MutateOption) (*domain.Device, *domain.Device, bool, error)
	ResumeDeltaIfCurrent(ctx context.Context, orgID uuid.UUID, name, specHash string) (bool, error)
}

// StorePreparingStatus owns the Fleet and Device status mutations used by the
// delta worker. Keeping this as a concrete helper makes the status flow
// explicit without introducing a status-service interface between worker
// components.
type StorePreparingStatus struct {
	fleets  fleetStatusStore
	devices deviceStatusStore
}

func NewStorePreparingStatus(fleets fleetStatusStore, devices deviceStatusStore) *StorePreparingStatus {
	return &StorePreparingStatus{fleets: fleets, devices: devices}
}

func (s *StorePreparingStatus) Set(ctx context.Context, orgId uuid.UUID, kind, name string, completed, total int) error {
	switch kind {
	case domain.FleetKind:
		return s.setFleet(ctx, orgId, name, completed, total)
	case domain.DeviceKind:
		return s.setDevice(ctx, orgId, name, completed, total)
	default:
		return fmt.Errorf("unsupported preparing status kind %q", kind)
	}
}

func (s *StorePreparingStatus) Clear(ctx context.Context, orgId uuid.UUID, kind, name string) error {
	switch kind {
	case domain.FleetKind:
		return s.clearFleet(ctx, orgId, name)
	case domain.DeviceKind:
		return s.clearDevice(ctx, orgId, name)
	default:
		return fmt.Errorf("unsupported preparing status kind %q", kind)
	}
}

// ResumeIfCurrent performs one conditional resource update. A missing result
// means either the identity changed or the preparing marker was already
// cleared; callers must stop without retrying or emitting a follow-up event.
func (s *StorePreparingStatus) ResumeIfCurrent(ctx context.Context, orgID uuid.UUID, kind, name string, identity ResumeIdentity) (ResumeResult, error) {
	switch kind {
	case domain.FleetKind:
		return s.resumeFleetIfCurrent(ctx, orgID, name, identity)
	case domain.DeviceKind:
		return s.resumeDeviceIfCurrent(ctx, orgID, name, identity)
	default:
		return ResumeResult{}, fmt.Errorf("unsupported preparing status kind %q", kind)
	}
}

func (s *StorePreparingStatus) resumeFleetIfCurrent(ctx context.Context, orgID uuid.UUID, name string, identity ResumeIdentity) (ResumeResult, error) {
	if s.fleets == nil || identity.TemplateVersion == nil {
		return ResumeResult{}, nil
	}
	updated, err := s.fleets.ResumeDeltaIfCurrent(ctx, orgID, name, *identity.TemplateVersion)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume fleet status: %w", err)
	}
	if updated == nil {
		return ResumeResult{}, nil
	}
	return ResumeResult{Matched: true, Fleet: updated}, nil
}

func (s *StorePreparingStatus) resumeDeviceIfCurrent(ctx context.Context, orgID uuid.UUID, name string, identity ResumeIdentity) (ResumeResult, error) {
	if s.devices == nil || identity.SpecHash == nil {
		return ResumeResult{}, nil
	}
	matched, err := s.devices.ResumeDeltaIfCurrent(ctx, orgID, name, *identity.SpecHash)
	if err != nil {
		return ResumeResult{}, fmt.Errorf("resume device status: %w", err)
	}
	return ResumeResult{Matched: matched}, nil
}

func (s *StorePreparingStatus) setFleet(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
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
	if err != nil {
		return fmt.Errorf("set fleet preparing status: %w", err)
	}
	return nil
}

func (s *StorePreparingStatus) clearFleet(ctx context.Context, orgId uuid.UUID, name string) error {
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
	if err != nil {
		return fmt.Errorf("clear fleet preparing status: %w", err)
	}
	return nil
}

func (s *StorePreparingStatus) setDevice(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
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
	if err != nil {
		return fmt.Errorf("set device preparing status: %w", err)
	}
	return nil
}

func (s *StorePreparingStatus) clearDevice(ctx context.Context, orgId uuid.UUID, name string) error {
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
	if err != nil {
		return fmt.Errorf("clear device preparing status: %w", err)
	}
	return nil
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
