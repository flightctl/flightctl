package tasks

import (
	"context"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

func fleetApplicationLifecycle(ctx context.Context, orgId uuid.UUID, event domain.Event, serviceHandler service.Service, log logrus.FieldLogger) error {
	logic := NewFleetApplicationLifecycleLogic(log, serviceHandler, orgId, event)
	err := logic.SyncFleet(ctx)
	if err != nil {
		log.Errorf("failed propagating fleet application lifecycle default for fleet %s/%s: %v", orgId, event.InvolvedObject.Name, err)
	}
	return err
}

// FleetApplicationLifecycleLogic propagates a change to a fleet's application lifecycle
// default to every device currently owned by the fleet: it refreshes each device's local
// cache of that default and triggers a re-render for each one.
type FleetApplicationLifecycleLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	orgId          uuid.UUID
	event          domain.Event
	itemsPerPage   int
}

func NewFleetApplicationLifecycleLogic(log logrus.FieldLogger, serviceHandler service.Service, orgId uuid.UUID, event domain.Event) FleetApplicationLifecycleLogic {
	return FleetApplicationLifecycleLogic{
		log:            log,
		serviceHandler: serviceHandler,
		orgId:          orgId,
		event:          event,
		itemsPerPage:   ItemsPerPage,
	}
}

func (f FleetApplicationLifecycleLogic) SyncFleet(ctx context.Context) error {
	fleetName := f.event.InvolvedObject.Name
	fleet, status := f.serviceHandler.GetFleet(ctx, f.orgId, fleetName, domain.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get fleet %s/%s: %s", f.orgId, fleetName, status.Message)
	}
	fleetRaw := lo.FromPtr(fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle]

	if f.event.Details == nil {
		return fmt.Errorf("application lifecycle changed event for fleet %s/%s has no details", f.orgId, fleetName)
	}
	details, err := f.event.Details.AsApplicationLifecycleChangedDetails()
	if err != nil {
		return fmt.Errorf("failed to decode application lifecycle changed details for fleet %s/%s: %w", f.orgId, fleetName, err)
	}

	owner := util.SetResourceOwner(domain.FleetKind, fleetName)
	listParams := domain.ListDevicesParams{
		Limit:         lo.ToPtr(int32(f.itemsPerPage)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}

	failureCount := 0
	for {
		iterCtx, cancel := fleetRolloutIterationContext(ctx, fleetRolloutIterationTimeout)
		devices, status := f.serviceHandler.ListDevices(iterCtx, f.orgId, listParams, nil)
		if status.Code != http.StatusOK {
			cancel()
			return fmt.Errorf("failed fetching devices for fleet %s/%s: %s", f.orgId, fleetName, status.Message)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			if err := f.syncDevice(iterCtx, device, fleetRaw, details); err != nil {
				f.log.Errorf("failed to propagate fleet %s/%s application lifecycle default to device %s: %v", f.orgId, fleetName, lo.FromPtr(device.Metadata.Name), err)
				failureCount++
			}
		}

		nextContinue := devices.Metadata.Continue
		cancel()
		if nextContinue == nil {
			break
		}
		listParams.Continue = nextContinue
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed propagating application lifecycle default to %d devices", failureCount)
	}
	return nil
}

// syncDevice refreshes deviceName's cached copy of the fleet's application lifecycle default
// and triggers a re-render. Devices without an application named details.AppName are
// unaffected.
func (f FleetApplicationLifecycleLogic) syncDevice(ctx context.Context, device *domain.Device, fleetRaw string, details domain.ApplicationLifecycleChangedDetails) error {
	deviceName := lo.FromPtr(device.Metadata.Name)

	annotations := map[string]string{}
	var deleteKeys []string
	if fleetRaw == "" {
		deleteKeys = []string{domain.DeviceAnnotationFleetApplicationLifecycle}
	} else {
		annotations[domain.DeviceAnnotationFleetApplicationLifecycle] = fleetRaw
	}
	status := f.serviceHandler.UpdateDeviceAnnotations(ctx, f.orgId, deviceName, annotations, deleteKeys)
	if status.Code != http.StatusOK {
		return service.ApiStatusToErr(status)
	}

	f.serviceHandler.CreateEvent(ctx, f.orgId, common.GetApplicationLifecycleChangedEvent(ctx, deviceName, details.AppName, details.Action))
	return nil
}
