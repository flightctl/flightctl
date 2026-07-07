package service

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
)

// StopFleetApplication sets a fleet-level default so that the named application's desiredState
// is "stopped" on every device currently owned by this fleet, independent of the application's
// declarative spec. Stamped with a fresh version (see domain.NewLifecycleVersion) so it can be
// arbitrated by recency against each device's own override for the same application at render
// time (see domain.OverlayApplicationLifecycle): this fleet-wide stop wins over an earlier
// device-level start, but a device-level start issued afterwards would in turn win over it.
func (h *ServiceHandler) StopFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status) {
	if status := h.validateFleetForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	err := h.store.Fleet().MutateAnnotation(ctx, orgId, name, domain.FleetAnnotationApplicationLifecycle, func(current string) (string, error) {
		return domain.MergeApplicationLifecycleOverrides(current, map[string]domain.ApplicationLifecycleOverride{
			appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateStopped, domain.NewLifecycleVersion()),
		})
	})
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	h.CreateEvent(ctx, orgId, common.GetFleetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStop))
	return h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
}

// StartFleetApplication sets a fleet-level default so that the named application's desiredState
// is "running" on every device currently owned by this fleet, independent of the application's
// declarative spec. Stamped with a fresh version (see domain.NewLifecycleVersion), just like
// StopFleetApplication, so this fleet-wide start wins over an earlier device-level stop, but a
// device-level stop issued afterwards would in turn win over it (see
// domain.OverlayApplicationLifecycle).
func (h *ServiceHandler) StartFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status) {
	if status := h.validateFleetForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	err := h.store.Fleet().MutateAnnotation(ctx, orgId, name, domain.FleetAnnotationApplicationLifecycle, func(current string) (string, error) {
		return domain.MergeApplicationLifecycleOverrides(current, map[string]domain.ApplicationLifecycleOverride{
			appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateRunning, domain.NewLifecycleVersion()),
		})
	})
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.FleetKind, &name)
	}

	h.CreateEvent(ctx, orgId, common.GetFleetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStart))
	return h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
}

// validateFleetForLifecycleAction fetches the fleet and validates it exists and has an
// application named appName in its device template.
func (h *ServiceHandler) validateFleetForLifecycleAction(ctx context.Context, orgId uuid.UUID, name string, appName string) domain.Status {
	fleet, status := h.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return status
	}
	if !domain.ApplicationsContainName(fleet.Spec.Template.Spec.Applications, appName) {
		return domain.StatusResourceNotFound("Application", appName)
	}
	return domain.StatusOK()
}
