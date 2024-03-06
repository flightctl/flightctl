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
			logic := NewFleetRolloutsLogic(taskManager, log, taskManager.store, resourceRef)

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
	taskManager  TaskManager
	log          logrus.FieldLogger
	fleetStore   store.Fleet
	devStore     store.Device
	tvStore      store.TemplateVersion
	resourceRef  ResourceReference
	itemsPerPage int
}

func NewFleetRolloutsLogic(tm TaskManager, log logrus.FieldLogger, storeInst store.Store, resourceRef ResourceReference) FleetRolloutsLogic {
	return FleetRolloutsLogic{
		taskManager:  tm,
		log:          log,
		fleetStore:   storeInst.Fleet(),
		devStore:     storeInst.Device(),
		tvStore:      storeInst.TemplateVersion(),
		resourceRef:  resourceRef,
		itemsPerPage: ItemsPerPage,
	}
}

func (f *FleetRolloutsLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

func (f FleetRolloutsLogic) RolloutFleet(ctx context.Context) error {
	f.log.Infof("Rolling out fleet %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	templateVersion, err := f.tvStore.GetNewestValid(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get templateVersion: %w", err)
	}

	failureCount := 0
	owner := util.SetResourceOwner(model.FleetKind, f.resourceRef.Name)

	listParams := store.ListParams{Owner: owner, Limit: ItemsPerPage}
	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			// TODO: Retry when we have a mechanism that allows it
			return fmt.Errorf("failed fetching devices: %w", err)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err = f.updateDeviceToFleetTemplate(ctx, device, templateVersion)
			if err != nil {
				f.log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, f.resourceRef.Name, err)
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

	ownerName, isFleetOwner, err := getOwnerFleet(device)
	if err != nil {
		return fmt.Errorf("failed getting device owner: %w", err)
	}
	if !isFleetOwner {
		return nil
	}

	templateVersion, err := f.tvStore.GetNewestValid(ctx, f.resourceRef.OrgID, ownerName)
	if err != nil {
		return fmt.Errorf("failed to get templateVersion: %w", err)
	}

	return f.updateDeviceToFleetTemplate(ctx, device, templateVersion)
}

func (f FleetRolloutsLogic) updateDeviceToFleetTemplate(ctx context.Context, device *api.Device, templateVersion *api.TemplateVersion) error {
	if device.Spec.TemplateVersion != nil && *device.Spec.TemplateVersion == *templateVersion.Metadata.Name {
		// Nothing to do
		return nil
	}

	f.log.Infof("Rolling out device %s/%s to templateVersion %s", f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name)

	return f.devStore.UpdateTemplateVersionAndOwner(ctx, f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name, nil, f.taskManager.DeviceUpdatedCallback)
}
