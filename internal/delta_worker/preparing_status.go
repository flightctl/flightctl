package delta_worker

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/google/uuid"
)

type fleetStatusStore interface {
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...fleetstore.GetOption) (*domain.Fleet, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, *domain.Fleet, error)
}

type deviceStatusStore interface {
	Get(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, error)
	ReplaceServiceOwnedStatus(ctx context.Context, orgId uuid.UUID, device *domain.Device) (*domain.Device, *domain.Device, error)
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

func (s *storePreparingStatus) SetProgress(ctx context.Context, orgId uuid.UUID, kind, name string, progress GenerationProgress) error {
	switch kind {
	case domain.FleetKind:
		return s.setFleetProgress(ctx, orgId, name, progress)
	case domain.DeviceKind:
		return s.setDeviceProgress(ctx, orgId, name, progress)
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
	fleet, err := s.fleets.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if fleet.Status == nil {
		fleet.Status = &domain.FleetStatus{}
	}
	domain.SetStatusCondition(&fleet.Status.Conditions, preparingCondition(domain.ConditionTypeFleetDeltaPreparing, completed, total))
	fleet.Status.DeltaGeneration = newDeltaGenerationStatus(completed, total)
	_, _, err = s.fleets.UpdateStatus(ctx, orgId, fleet)
	return err
}

func (s *storePreparingStatus) setFleetProgress(ctx context.Context, orgId uuid.UUID, name string, progress GenerationProgress) error {
	if s.fleets == nil {
		return fmt.Errorf("fleet store is required")
	}
	fleet, err := s.fleets.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if fleet.Status == nil || fleet.Status.DeltaGeneration == nil {
		return nil
	}
	completed := int(fleet.Status.DeltaGeneration.Completed)
	total := int(fleet.Status.DeltaGeneration.Total)
	applyGenerationProgress(fleet.Status.DeltaGeneration, progress)
	domain.SetStatusCondition(&fleet.Status.Conditions, preparingCondition(domain.ConditionTypeFleetDeltaPreparing, completed, total))
	_, _, err = s.fleets.UpdateStatus(ctx, orgId, fleet)
	return err
}

func (s *storePreparingStatus) clearFleet(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.fleets == nil {
		return fmt.Errorf("fleet store is required")
	}
	fleet, err := s.fleets.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if fleet.Status == nil {
		return nil
	}
	domain.RemoveStatusCondition(&fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
	fleet.Status.DeltaGeneration = nil
	_, _, err = s.fleets.UpdateStatus(ctx, orgId, fleet)
	return err
}

func (s *storePreparingStatus) setDevice(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	device, err := s.devices.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if device.Status == nil {
		device.Status = &domain.DeviceStatus{}
	}
	domain.SetStatusCondition(&device.Status.Conditions, preparingCondition(domain.ConditionTypeDeviceDeltaPreparing, completed, total))
	device.Status.DeltaGeneration = newDeltaGenerationStatus(completed, total)
	_, _, err = s.devices.ReplaceServiceOwnedStatus(ctx, orgId, device)
	return err
}

func (s *storePreparingStatus) setDeviceProgress(ctx context.Context, orgId uuid.UUID, name string, progress GenerationProgress) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	device, err := s.devices.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if device.Status == nil || device.Status.DeltaGeneration == nil {
		return nil
	}
	completed := int(device.Status.DeltaGeneration.Completed)
	total := int(device.Status.DeltaGeneration.Total)
	applyGenerationProgress(device.Status.DeltaGeneration, progress)
	domain.SetStatusCondition(&device.Status.Conditions, preparingCondition(domain.ConditionTypeDeviceDeltaPreparing, completed, total))
	_, _, err = s.devices.ReplaceServiceOwnedStatus(ctx, orgId, device)
	return err
}

func (s *storePreparingStatus) clearDevice(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	device, err := s.devices.Get(ctx, orgId, name)
	if err != nil {
		return err
	}
	if device.Status == nil {
		return nil
	}
	domain.RemoveStatusCondition(&device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
	device.Status.DeltaGeneration = nil
	_, _, err = s.devices.ReplaceServiceOwnedStatus(ctx, orgId, device)
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

func applyGenerationProgress(dst *domain.DeltaGenerationStatus, p GenerationProgress) {
	dst.Phase = nil
	dst.Percent = nil
	dst.BytesDone = nil
	dst.BytesTotal = nil
	dst.ItemsDone = nil
	dst.ItemsTotal = nil
	now := time.Now().UTC()
	dst.LastUpdated = &now
	if p.Phase != "" {
		ph := p.Phase
		dst.Phase = &ph
	}
	dst.Percent = p.Percent
	dst.BytesDone = p.BytesDone
	dst.BytesTotal = p.BytesTotal
	dst.ItemsDone = p.ItemsDone
	dst.ItemsTotal = p.ItemsTotal
}

func preparingCondition(condType domain.ConditionType, completed, total int) domain.Condition {
	return domain.Condition{
		Type:    condType,
		Status:  domain.ConditionStatusTrue,
		Reason:  string(condType),
		Message: fmt.Sprintf("%d/%d", completed, total),
	}
}
