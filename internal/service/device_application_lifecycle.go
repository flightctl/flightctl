package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// GetDeviceApplicationLifecycle returns the device-level lifecycle control override for an
// application, if one has been set.
func (h *ServiceHandler) GetDeviceApplicationLifecycle(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.DeviceApplicationLifecycle, domain.Status) {
	device, status := h.GetDevice(ctx, orgId, name)
	if status.Code != http.StatusOK {
		return nil, status
	}
	lifecycles, err := decodeApplicationLifecycleMap(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle])
	if err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}
	entry, ok := lifecycles[appName]
	if !ok {
		return nil, domain.StatusResourceNotFound("DeviceApplicationLifecycle", appName)
	}
	return &entry, domain.StatusOK()
}

// SetDeviceApplicationDesiredState sets the device-level desired lifecycle state for an
// application, independent of the application's declarative spec.
func (h *ServiceHandler) SetDeviceApplicationDesiredState(ctx context.Context, orgId uuid.UUID, name string, appName string, desiredState domain.ApplicationDesiredState) (*domain.DeviceApplicationLifecycle, domain.Status) {
	return h.modifyDeviceApplicationLifecycle(ctx, orgId, name, appName, true, func(existing domain.DeviceApplicationLifecycle) domain.DeviceApplicationLifecycle {
		existing.DesiredState = &desiredState
		return existing
	})
}

// RestartDeviceApplication increments the application's restartGeneration counter, signaling
// the agent to restart it. Only meaningful while the application's desired state is "running".
func (h *ServiceHandler) RestartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.DeviceApplicationLifecycle, domain.Status) {
	return h.modifyDeviceApplicationLifecycle(ctx, orgId, name, appName, true, func(existing domain.DeviceApplicationLifecycle) domain.DeviceApplicationLifecycle {
		existing.RestartGeneration = lo.ToPtr(lo.FromPtr(existing.RestartGeneration) + 1)
		return existing
	})
}

// DeleteDeviceApplicationLifecycle clears the device-level lifecycle override for an
// application, reverting it to whatever its declarative spec says.
func (h *ServiceHandler) DeleteDeviceApplicationLifecycle(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.DeviceApplicationLifecycle, domain.Status) {
	return h.modifyDeviceApplicationLifecycle(ctx, orgId, name, appName, false, nil)
}

// modifyDeviceApplicationLifecycle atomically reads, mutates, and persists the per-application
// entry of the device's application lifecycle annotation, retrying on resource version
// conflicts. If mutate is nil, the entry for appName is removed instead. The annotation is
// the sole source of truth for lifecycle overrides: it is overlaid onto the rendered
// application spec at serve time for both standalone and fleet-owned devices (see
// model.Device.ToApiResource), and onto a fleet-owned device's own spec whenever it is
// next rolled out (see FleetRolloutsLogic.getDeviceApps), so it survives fleet template
// rollouts.
func (h *ServiceHandler) modifyDeviceApplicationLifecycle(ctx context.Context, orgId uuid.UUID, deviceName, appName string, requireAppExists bool, mutate func(existing domain.DeviceApplicationLifecycle) domain.DeviceApplicationLifecycle) (*domain.DeviceApplicationLifecycle, domain.Status) {
	var (
		err                 error
		result              domain.DeviceApplicationLifecycle
		nextRenderedVersion string
	)
	for i := 0; i != 10; i++ {
		device, status := h.GetDevice(ctx, orgId, deviceName)
		if status.Code != http.StatusOK {
			return nil, status
		}
		device.Metadata.Annotations = lo.ToPtr(util.EnsureMap(lo.FromPtr(device.Metadata.Annotations)))
		annotations := lo.FromPtr(device.Metadata.Annotations)

		if waitingValue, exists := annotations[domain.DeviceAnnotationAwaitingReconnect]; exists && waitingValue == "true" {
			return nil, domain.StatusConflict("Device is awaiting reconnection after restore")
		}
		if pausedValue, exists := annotations[domain.DeviceAnnotationConflictPaused]; exists && pausedValue == "true" {
			return nil, domain.StatusConflict("Device is paused due to conflicts")
		}
		if device.Spec != nil && device.Spec.Decommissioning != nil {
			return nil, domain.StatusConflict("Device is decommissioned")
		}
		if requireAppExists && !deviceHasApplication(device, appName) {
			return nil, domain.StatusResourceNotFound("Application", appName)
		}

		lifecycles, decodeErr := decodeApplicationLifecycleMap(annotations[domain.DeviceAnnotationApplicationLifecycle])
		if decodeErr != nil {
			return nil, domain.StatusInternalServerError(decodeErr.Error())
		}

		if mutate != nil {
			result = mutate(lifecycles[appName])
			lifecycles[appName] = result
		} else {
			result = domain.DeviceApplicationLifecycle{}
			delete(lifecycles, appName)
		}

		if len(lifecycles) == 0 {
			delete(*device.Metadata.Annotations, domain.DeviceAnnotationApplicationLifecycle)
		} else {
			encoded, encodeErr := json.Marshal(lifecycles)
			if encodeErr != nil {
				return nil, domain.StatusInternalServerError(encodeErr.Error())
			}
			(*device.Metadata.Annotations)[domain.DeviceAnnotationApplicationLifecycle] = string(encoded)
		}

		nextRenderedVersion, err = domain.GetNextDeviceRenderedVersion(*device.Metadata.Annotations, device.Status)
		if err != nil {
			return nil, domain.StatusInternalServerError(err.Error())
		}
		(*device.Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion] = nextRenderedVersion

		_, err = h.UpdateDevice(context.WithValue(ctx, consts.InternalRequestCtxKey, true), orgId, deviceName, *device, nil)
		if !errors.Is(err, flterrors.ErrResourceVersionConflict) {
			break
		}
	}
	if err == nil {
		err = rendered.Bus.Instance().StoreAndNotify(ctx, orgId, deviceName, nextRenderedVersion)
	}
	if err != nil {
		return nil, domain.StatusInternalServerError(err.Error())
	}
	return &result, domain.StatusOK()
}

func decodeApplicationLifecycleMap(value string) (map[string]domain.DeviceApplicationLifecycle, error) {
	lifecycles := map[string]domain.DeviceApplicationLifecycle{}
	if value == "" {
		return lifecycles, nil
	}
	if err := json.Unmarshal([]byte(value), &lifecycles); err != nil {
		return nil, fmt.Errorf("failed to unmarshal application lifecycle annotation: %w", err)
	}
	return lifecycles, nil
}

func deviceHasApplication(device *domain.Device, appName string) bool {
	if device.Spec == nil || device.Spec.Applications == nil {
		return false
	}
	for _, app := range *device.Spec.Applications {
		if n, err := app.GetName(); err == nil && lo.FromPtr(n) == appName {
			return true
		}
	}
	return false
}
