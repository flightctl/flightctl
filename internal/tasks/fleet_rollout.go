package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
)

// Wait to be notified via channel about fleet template updates, exit upon ctx.Done()
func FleetRollouts(taskManager TaskManager) {
	for {
		select {
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		case resourceRef := <-taskManager.channels[ChannelFleetTemplateRollout]:
			requestID := reqid.NextRequestID()
			ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
			log := log.WithReqIDFromCtx(ctx, taskManager.log)
			logic := FleetRolloutsLogic{
				log:         log,
				fleetStore:  taskManager.store.Fleet(),
				devStore:    taskManager.store.Device(),
				resourceRef: resourceRef,
			}

			if resourceRef.Op != FleetRolloutOpUpdate {
				taskManager.log.Errorf("received unknown op %s", resourceRef.Op)
				break
			}
			if resourceRef.Kind == model.FleetKind {
				err := logic.RolloutFleet(ctx)
				if err != nil {
					taskManager.log.Errorf("failed rolling out fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			} else if resourceRef.Kind == model.DeviceKind {
				err := logic.RolloutDevice(ctx)
				if err != nil {
					taskManager.log.Errorf("failed rolling out device %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
				}
			} else {
				taskManager.log.Errorf("FleetRollouts called with incorrect resource kind %s", resourceRef.Kind)
			}
		}
	}
}

type FleetRolloutsLogic struct {
	log          logrus.FieldLogger
	fleetStore   store.Fleet
	devStore     store.Device
	resourceRef  ResourceReference
	itemsPerPage int
}

func NewFleetRolloutsLogic(log logrus.FieldLogger, storeInst store.Store, resourceRef ResourceReference) FleetRolloutsLogic {
	return FleetRolloutsLogic{
		log:          log,
		fleetStore:   storeInst.Fleet(),
		devStore:     storeInst.Device(),
		resourceRef:  resourceRef,
		itemsPerPage: ItemsPerPage,
	}
}

func (f *FleetRolloutsLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

func (f FleetRolloutsLogic) RolloutFleet(ctx context.Context) error {
	f.log.Infof("Rolling out fleet %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	fleet, err := f.fleetStore.Get(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get fleet: %w", err)
	}

	// If there is no template set in the fleet, then there is nothing to sync to the devices
	if fleet.Spec.Template.Metadata == nil {
		f.log.Warn("fleet does not have a template")
		return nil
	}

	failureCount := 0
	owner := util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name)
	listParams := store.ListParams{Owner: owner, Limit: ItemsPerPage}
	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			// TODO: Retry when we have a mechanism that allows it
			return fmt.Errorf("failed fetching devices: %w", err)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err = f.updateDeviceSpecAccordingToFleetTemplate(ctx, device, fleet)
			if err != nil {
				f.log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, *fleet.Metadata.Name, err)
				failureCount++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed updating %d devices", failureCount)
	}

	return nil
}

// The device's owner was changed, roll out if necessary
func (f FleetRolloutsLogic) RolloutDevice(ctx context.Context) error {
	f.log.Infof("Rolling out device %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	device, err := f.devStore.Get(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	if device.Metadata.Owner == nil || len(*device.Metadata.Owner) == 0 {
		return nil
	}

	if device.Metadata.Annotations != nil {
		multipleOwners, ok := (*device.Metadata.Annotations)[model.DeviceAnnotationMultipleOwners]
		if ok && len(multipleOwners) > 0 {
			f.log.Warnf("Device has multiple owners, skipping rollout: %s", multipleOwners)
		}
	}

	owner, isFleetOwner, err := getOwnerFleet(device)
	if err != nil {
		return fmt.Errorf("failed getting device owner: %w", err)
	}
	if !isFleetOwner {
		return nil
	}

	fleet, err := f.fleetStore.Get(ctx, f.resourceRef.OrgID, owner)
	if err != nil {
		return fmt.Errorf("failed to get fleet %s: %w", owner, err)
	}

	return f.updateDeviceSpecAccordingToFleetTemplate(ctx, device, fleet)
}

func (f FleetRolloutsLogic) updateDeviceSpecAccordingToFleetTemplate(ctx context.Context, device *api.Device, fleet *api.Fleet) error {
	if fleet.Spec.Template.Metadata == nil {
		return nil
	}
	targetGeneration := *fleet.Spec.Template.Metadata.Generation
	if device.Metadata.Generation != nil && *device.Metadata.Generation == targetGeneration {
		// Nothing to do
		return nil
	}

	f.log.Infof("Rolling out device %s/%s to generation %d", f.resourceRef.OrgID, *device.Metadata.Name, targetGeneration)
	return f.devStore.UpdateSpec(ctx, f.resourceRef.OrgID, *device.Metadata.Name, targetGeneration, fleet.Spec.Template.Spec)
}
