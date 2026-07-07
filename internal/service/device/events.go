package device

import (
	"context"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
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
		// The 5th parameter (st store.Store) of ComputeDeviceStatusChanges is provably dead
		// code (never dereferenced in its body) and event.Store has no relationship to the
		// full store.Store aggregate, so nil is passed here instead of a store value. See
		// 01-context.md's Discrepancy note for the full rationale.
		statusUpdates := common.ComputeDeviceStatusChanges(ctx, oldDevice, newDevice, orgId, nil)

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
	} else {
		updateDetails := common.ComputeResourceUpdatedDetails(oldDevice.Metadata, newDevice.Metadata)
		// Generate ResourceUpdated event if there are spec changes or status changes
		if updateDetails != nil {
			annotations := map[string]string{}
			delayDeviceRender, ok := ctx.Value(consts.DelayDeviceRenderCtxKey).(bool)
			if ok && delayDeviceRender {
				annotations[domain.EventAnnotationDelayDeviceRender] = "true"
			}

			eventsService.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.DeviceKind, name, updateDetails, log, annotations))
		}
	}
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
