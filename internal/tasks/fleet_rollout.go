package tasks

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

/* Wait to be notified via channel about fleet template updates, exit upon ctx.Done() */
func FleetRollouts(taskManager TaskManager) {
	reqid.OverridePrefix("fleet-rollout")

	for {
		select {
		case fleetRef := <-taskManager.channels[FleetTemplateRollout]:
			err := rolloutFleet(taskManager.log, taskManager.store.Fleet(), taskManager.store.Device(), fleetRef)
			if err != nil {
				taskManager.log.Errorf("failed rolling out fleet %s/%s: %v", fleetRef.OrgID, fleetRef.Name, err)
			}
		case <-taskManager.ctx.Done():
			taskManager.log.Info("Received ctx.Done(), stopping")
			return
		}
	}
}

func rolloutFleet(baseLog logrus.FieldLogger, fleetStore store.Fleet, devStore store.Device, fleetRef ResourceReference) error {
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, baseLog)

	log.Infof("Rolling out fleet %s/%s", fleetRef.OrgID, fleetRef.Name)

	fleet, err := fleetStore.Get(ctx, fleetRef.OrgID, fleetRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get fleet: %w", err)
	}

	// If there is no template set in the fleet, then there is nothing to sync to the devices
	if fleet.Spec.Template.Metadata == nil {
		log.Warn("fleet does not have a template")
		return nil
	}

	failureCount := 0
	for {
		devices, err := devStore.List(ctx, fleetRef.OrgID, store.ListParams{Labels: fleet.Spec.Selector.MatchLabels, Limit: ItemsPerPage})
		if err != nil {
			// TODO: Retry when we have a mechanism that allows it
			return fmt.Errorf("failed fetching devices: %w", err)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err = updateDeviceSpecAccordingToFleetTemplate(ctx, log, devStore, fleetRef.OrgID, device, fleet)
			if err != nil {
				log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, *fleet.Metadata.Name, err)
				failureCount++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed updating %d devices", failureCount)
	}

	return nil
}

func updateDeviceSpecAccordingToFleetTemplate(ctx context.Context, log logrus.FieldLogger, devStore store.Device, orgId uuid.UUID, device *api.Device, fleet *api.Fleet) error {
	if fleet.Spec.Template.Metadata == nil {
		return nil
	}
	targetGeneration := *fleet.Spec.Template.Metadata.Generation
	if device.Metadata.Generation != nil && *device.Metadata.Generation == targetGeneration {
		// Nothing to do
		return nil
	}

	return devStore.UpdateDeviceSpec(ctx, orgId, *device.Metadata.Name, targetGeneration, fleet.Spec.Template.Spec)
}
