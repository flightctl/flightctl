package device

import (
	"context"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// EmitDeviceUpdatedEvent handles all device-related event emission logic for a device
// create/update. Exported so packages that create devices as a side effect of their own
// operation (e.g. internal/service/enrollmentrequest, approving an enrollment request) can
// emit the same device-updated event a direct device update would, without depending on a
// generic events hub for device-specific decisions.
func EmitDeviceUpdatedEvent(ctx context.Context, eventsService events.Service, log logrus.FieldLogger, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
		eventsService.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.DeviceKind, name, status, nil))
		return
	}
	var (
		oldDevice, newDevice *domain.Device
		ok                   bool
	)
	if oldDevice, newDevice, ok = common.CastResources[domain.Device](oldResource, newResource); !ok {
		return
	}

	// Only generate status change events when the device is not being created
	if !created {
		statusUpdates := common.ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId)

		// Deduplicate DeviceDisconnected events - if multiple status fields changed to Unknown,
		// only emit one DeviceDisconnected event
		deviceDisconnectedEmitted := false
		for _, update := range statusUpdates {
			if update.Reason == domain.EventReasonDeviceDisconnected {
				if !deviceDisconnectedEmitted {
					eventsService.CreateEvent(ctx, orgId, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
					deviceDisconnectedEmitted = true
				}
			} else {
				eventsService.CreateEvent(ctx, orgId, common.GetDeviceEventFromUpdateDetails(ctx, name, update))
			}
		}
	}

	// Generate resource creation/update events
	if created {
		eventsService.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, true, domain.DeviceKind, name, nil, log, nil))
		return
	}
	if oldDevice == nil || newDevice == nil {
		return
	}

	updateDetails := common.ComputeResourceUpdatedDetails(oldDevice.Metadata, newDevice.Metadata)
	// Spec changes must trigger ResourceUpdated even when Generation is missing/unchanged
	// on the before/after snapshots (render tasks key off UpdatedFields=Spec).
	updateDetails = ensureSpecUpdatedField(updateDetails, oldDevice, newDevice)
	if updateDetails == nil {
		return
	}

	annotations := map[string]string{}
	delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
	holdStandalone := deviceSpecsChanged(oldDevice, newDevice) && !hasFleetOwner(newDevice)
	if (ok && delayDeviceRender) || holdStandalone {
		annotations[domain.EventAnnotationDelayDeviceRender] = "true"
	}
	eventsService.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.DeviceKind, name, updateDetails, log, annotations))
	if holdStandalone {
		emitStandalonePrepareDeltas(ctx, eventsService, orgId, name)
	}
}

func hasFleetOwner(device *domain.Device) bool {
	if device == nil {
		return false
	}
	kind, _, err := util.GetResourceOwner(device.Metadata.Owner)
	return err == nil && kind == domain.FleetKind
}

func emitStandalonePrepareDeltas(ctx context.Context, eventsService events.Service, orgId uuid.UUID, name string) {
	details := domain.PrepareDeltasDetails{
		DetailType: domain.PrepareDeltasDetailsDetailType("PrepareDeltas"),
	}
	var eventDetails domain.EventDetails
	if err := eventDetails.FromPrepareDeltasDetails(details); err != nil {
		return
	}
	eventsService.CreateEvent(ctx, orgId, domain.GetBaseEvent(ctx, domain.DeviceKind, name, domain.EventReasonPrepareDeltas, "Preparing OS image deltas", &eventDetails))
}

func ensureSpecUpdatedField(details *domain.ResourceUpdatedDetails, oldDevice, newDevice *domain.Device) *domain.ResourceUpdatedDetails {
	if !deviceSpecsChanged(oldDevice, newDevice) {
		return details
	}
	if details == nil {
		details = &domain.ResourceUpdatedDetails{}
	}
	for _, field := range details.UpdatedFields {
		if field == domain.Spec {
			return details
		}
	}
	details.UpdatedFields = append(details.UpdatedFields, domain.Spec)
	return details
}

func deviceSpecsChanged(oldDevice, newDevice *domain.Device) bool {
	oldSpec := oldDevice.Spec
	newSpec := newDevice.Spec
	if oldSpec == nil && newSpec == nil {
		return false
	}
	if oldSpec == nil || newSpec == nil {
		return true
	}
	return !domain.DeviceSpecsAreEqual(*oldSpec, *newSpec)
}

// EmitDeviceDecommissionEvent handles device decommission event emission logic.
func EmitDeviceDecommissionEvent(ctx context.Context, eventsService events.Service, _ domain.ResourceKind, orgId uuid.UUID, name string, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, domain.DeviceKind, &name)
		eventsService.CreateEvent(ctx, orgId, common.GetDeviceDecommissionedFailureEvent(ctx, created, domain.DeviceKind, name, status))
	} else {
		eventsService.CreateEvent(ctx, orgId, common.GetDeviceDecommissionedSuccessEvent(ctx, created, domain.DeviceKind, name, nil, nil))
	}
}
