package device

import (
	"context"
	"errors"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

var errApplicationNotInSpec = errors.New("application not in device spec")

// pruneLifecycleOnCurrent drops lifecycle overrides for apps no longer in Spec.Applications.
// Decode/prune errors are ignored so a corrupt annotation cannot block the spec write.
func pruneLifecycleOnCurrent(log logrus.FieldLogger, device *domain.Device) error {
	if device == nil {
		return nil
	}
	var apps *[]domain.ApplicationProviderSpec
	if device.Spec != nil {
		apps = device.Spec.Applications
	}
	pruned, changed, err := domain.PruneApplicationLifecycleAnnotationMap(
		lo.FromPtr(device.Metadata.Annotations),
		apps,
		domain.DeviceAnnotationApplicationLifecycle,
		domain.DeviceAnnotationFleetApplicationLifecycle,
	)
	if err != nil {
		if log != nil {
			log.WithError(err).Warnf("skipping application lifecycle prune for device %s", lo.FromPtr(device.Metadata.Name))
		}
		return nil
	}
	if !changed {
		return nil
	}
	device.Metadata.Annotations = &pruned
	return nil
}

func rejectDecommissionedDevice(current *domain.Device) error {
	if current != nil && current.Spec != nil && current.Spec.Decommissioning != nil {
		return flterrors.ErrDecommission
	}
	return nil
}

// ensureDeviceLifecycleMutable re-checks lifecycle preconditions against the CAS-fresh device
// so concurrent decommission/pause/template changes cannot sneak past the pre-read.
func ensureDeviceLifecycleMutable(device *domain.Device, appName string) error {
	if err := rejectDecommissionedDevice(device); err != nil {
		return err
	}
	if device == nil {
		return errApplicationNotInSpec
	}
	annotations := lo.FromPtr(device.Metadata.Annotations)
	if annotations[domain.DeviceAnnotationAwaitingReconnect] == "true" {
		return flterrors.ErrDeviceAwaitingReconnect
	}
	if annotations[domain.DeviceAnnotationConflictPaused] == "true" {
		return flterrors.ErrDeviceConflictPaused
	}
	if device.Spec == nil || !domain.ApplicationsContainName(device.Spec.Applications, appName) {
		return errApplicationNotInSpec
	}
	return nil
}

func lifecycleActionStatus(err error, kind string, name, appName string) domain.Status {
	if errors.Is(err, errApplicationNotInSpec) {
		return domain.StatusResourceNotFound("Application", appName)
	}
	if errors.Is(err, flterrors.ErrDecommission) ||
		errors.Is(err, flterrors.ErrDeviceAwaitingReconnect) ||
		errors.Is(err, flterrors.ErrDeviceConflictPaused) {
		return domain.StatusBadRequest(err.Error())
	}
	return common.StoreErrorToApiStatus(err, false, kind, &name)
}

// StopDeviceApplication sets a device-level override so that the named application's
// desiredState is "stopped", independent of the application's declarative spec. The override
// is stamped with a fresh version so it wins over an earlier fleet-level default for the same
// application, as long as no more recent fleet-level action has been taken since.
func (h *DeviceServiceHandler) StopDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	_, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := ensureDeviceLifecycleMutable(m.Device, appName); err != nil {
			return err
		}
		ann, err := domain.MergeApplicationLifecycleOverrides(
			lo.FromPtr(m.Device.Metadata.Annotations),
			domain.DeviceAnnotationApplicationLifecycle,
			map[string]domain.ApplicationLifecycleOverride{
				appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateStopped, domain.NewLifecycleVersion()),
			},
		)
		if err != nil {
			return err
		}
		m.Device.Metadata.Annotations = &ann
		return nil
	})
	if err != nil {
		return nil, lifecycleActionStatus(err, domain.DeviceKind, name, appName)
	}

	h.events.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStop))
	return h.GetDevice(ctx, orgId, name)
}

// StartDeviceApplication sets a device-level override so that the named application's
// desiredState is "running", independent of the application's declarative spec. Same
// recency-based arbitration against the fleet-level default as StopDeviceApplication.
func (h *DeviceServiceHandler) StartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	_, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := ensureDeviceLifecycleMutable(m.Device, appName); err != nil {
			return err
		}
		ann, err := domain.MergeApplicationLifecycleOverrides(
			lo.FromPtr(m.Device.Metadata.Annotations),
			domain.DeviceAnnotationApplicationLifecycle,
			map[string]domain.ApplicationLifecycleOverride{
				appName: domain.NewDesiredStateOverride(domain.ApplicationDesiredStateRunning, domain.NewLifecycleVersion()),
			},
		)
		if err != nil {
			return err
		}
		m.Device.Metadata.Annotations = &ann
		return nil
	})
	if err != nil {
		return nil, lifecycleActionStatus(err, domain.DeviceKind, name, appName)
	}

	h.events.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionStart))
	return h.GetDevice(ctx, orgId, name)
}

// RestartDeviceApplication increments the device-level restartGeneration override for the
// named application, atomically against concurrent restarts. restartGeneration is
// device-only and left untouched by stop/start, so it's safe to simply increment whatever is
// currently stored.
func (h *DeviceServiceHandler) RestartDeviceApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
	if _, status := h.getDeviceForLifecycleAction(ctx, orgId, name, appName); status.Code != http.StatusOK {
		return nil, status
	}

	_, _, _, err := h.deviceStore.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		if err := ensureDeviceLifecycleMutable(m.Device, appName); err != nil {
			return err
		}
		annotations := lo.FromPtr(m.Device.Metadata.Annotations)
		currentGeneration, err := domain.GetApplicationRestartGeneration(annotations[domain.DeviceAnnotationApplicationLifecycle], appName)
		if err != nil {
			return err
		}
		ann, err := domain.MergeApplicationLifecycleOverrides(
			annotations,
			domain.DeviceAnnotationApplicationLifecycle,
			map[string]domain.ApplicationLifecycleOverride{
				appName: domain.NewRestartGenerationOverride(currentGeneration + 1),
			},
		)
		if err != nil {
			return err
		}
		m.Device.Metadata.Annotations = &ann
		return nil
	})
	if err != nil {
		return nil, lifecycleActionStatus(err, domain.DeviceKind, name, appName)
	}

	h.events.CreateEvent(ctx, orgId, common.GetApplicationLifecycleChangedEvent(ctx, name, appName, domain.ApplicationLifecycleActionRestart))
	return h.GetDevice(ctx, orgId, name)
}

// getDeviceForLifecycleAction fetches the device and validates it exists, is not
// decommissioned, is not awaiting reconnection or conflict-paused after a restore, and has an
// application named appName in its declarative spec.
func (h *DeviceServiceHandler) getDeviceForLifecycleAction(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Device, domain.Status) {
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
