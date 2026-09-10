package delta_worker

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/google/uuid"
)

type storePreparingStatus struct {
	fleets  fleetservice.Service
	devices deviceservice.Service
}

func NewServicePreparingStatus(fleets fleetservice.Service, devices deviceservice.Service) PreparingStatus {
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
	fleet, status := s.fleets.GetFleetStatus(ctx, orgId, name)
	if err := statusError(status); err != nil {
		return err
	}
	if fleet.Status == nil {
		fleet.Status = &domain.FleetStatus{}
	}
	domain.SetStatusCondition(&fleet.Status.Conditions, preparingCondition(domain.ConditionTypeFleetDeltaPreparing, completed, total))
	fleet.Status.DeltaGeneration = newDeltaGenerationStatus(completed, total)
	_, status = s.fleets.ReplaceFleetStatus(ctx, orgId, name, *fleet)
	return statusError(status)
}

func (s *storePreparingStatus) clearFleet(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.fleets == nil {
		return fmt.Errorf("fleet store is required")
	}
	fleet, status := s.fleets.GetFleetStatus(ctx, orgId, name)
	if err := statusError(status); err != nil {
		return err
	}
	if fleet.Status == nil {
		return nil
	}
	domain.RemoveStatusCondition(&fleet.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)
	fleet.Status.DeltaGeneration = nil
	_, status = s.fleets.ReplaceFleetStatus(ctx, orgId, name, *fleet)
	return statusError(status)
}

func (s *storePreparingStatus) setDevice(ctx context.Context, orgId uuid.UUID, name string, completed, total int) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	device, status := s.devices.GetDevice(ctx, orgId, name)
	if err := statusError(status); err != nil {
		return err
	}
	if device.Status == nil {
		device.Status = &domain.DeviceStatus{}
	}
	domain.SetStatusCondition(&device.Status.Conditions, preparingCondition(domain.ConditionTypeDeviceDeltaPreparing, completed, total))
	device.Status.DeltaGeneration = newDeltaGenerationStatus(completed, total)
	_, status = s.devices.ReplaceDeviceStatus(ctx, orgId, name, *device, false)
	return statusError(status)
}

func (s *storePreparingStatus) clearDevice(ctx context.Context, orgId uuid.UUID, name string) error {
	if s.devices == nil {
		return fmt.Errorf("device store is required")
	}
	device, status := s.devices.GetDevice(ctx, orgId, name)
	if err := statusError(status); err != nil {
		return err
	}
	if device.Status == nil {
		return nil
	}
	domain.RemoveStatusCondition(&device.Status.Conditions, domain.ConditionTypeDeviceDeltaPreparing)
	device.Status.DeltaGeneration = nil
	_, status = s.devices.ReplaceDeviceStatus(ctx, orgId, name, *device, false)
	return statusError(status)
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
