package service

import (
	"context"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// StopDeviceApplication sets a device-level override so that the named application's
// desiredState is "stopped", independent of the application's declarative spec. The override
// is stamped with a fresh version so it wins over an earlier fleet-level default for the same
// application, as long as no more recent fleet-level action has been taken since.
func (h *ServiceHandler) StopDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	err := h.store.Device().MutateAnnotation(ctx, orgId, name, domain.DeviceAnnotationApplicationLifecycle, func(current string) (string, error) {
		return domain.MergeApplicationLifecycleOverrides(current, map[string]domain.ApplicationLifecycleOverride{
			appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateStopped, domain.NewLifecycleVersion()),
		})
	})
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	h.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStop))
	return h.GetDevice(ctx, orgId, name)
}

// StartDeviceApplication sets a device-level override so that the named application's
// desiredState is "running", independent of the application's declarative spec. Same
// recency-based arbitration against the fleet-level default as StopDeviceApplication.
func (h *ServiceHandler) StartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	err := h.store.Device().MutateAnnotation(ctx, orgId, name, domain.DeviceAnnotationApplicationLifecycle, func(current string) (string, error) {
		return domain.MergeApplicationLifecycleOverrides(current, map[string]domain.ApplicationLifecycleOverride{
			appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateRunning, domain.NewLifecycleVersion()),
		})
	})
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	h.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStart))
	return h.GetDevice(ctx, orgId, name)
}

// RestartDeviceApplication increments the device-level restartGeneration override for the
// named application, atomically against concurrent restarts. restartGeneration is
// device-only and left untouched by stop/start, so it's safe to simply increment whatever is
// currently stored.
func (h *ServiceHandler) RestartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	err := h.store.Device().MutateAnnotation(ctx, orgId, name, domain.DeviceAnnotationApplicationLifecycle, func(current string) (string, error) {
		currentGeneration, err := domain.GetApplicationRestartGeneration(current, appName)
		if err != nil {
			return "", err
		}
		return domain.MergeApplicationLifecycleOverrides(current, map[string]domain.ApplicationLifecycleOverride{
			appName: domain.NewRestartGenerationOverride(currentGeneration + 1),
		})
	})
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, domain.DeviceKind, &name)
	}

	h.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionRestart))
	return h.GetDevice(ctx, orgId, name)
}

// getDeviceForLifecycleAction fetches the device and validates it exists, is not
// decommissioned, is not awaiting reconnection or conflict-paused after a restore, and has an
// application named appName in its declarative spec.
func (h *ServiceHandler) getDeviceForLifecycleAction(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	device, status := h.GetDevice(ctx, orgId, name)
	if status.Code != http.StatusOK {
		return nil, status
	}
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		return nil, domain.StatusBadRequest(flterrors.ErrDecommission.Error())
	}
	annotations := lo.FromPtr(device.Metadata.Annotations)
	if annotations[domain.DeviceAnnotationAwaitingReconnect] == "true" {
		return nil, domain.StatusBadRequest(flterrors.ErrDeviceAwaitingReconnect.Error())
	}
	if annotations[domain.DeviceAnnotationConflictPaused] == "true" {
		return nil, domain.StatusBadRequest(flterrors.ErrDeviceConflictPaused.Error())
	}
	if device.Spec == nil || !domain.ApplicationsContainName(device.Spec.Applications, appName) {
		return nil, domain.StatusResourceNotFound("Application", appName)
	}
	return device, domain.StatusOK()
}
