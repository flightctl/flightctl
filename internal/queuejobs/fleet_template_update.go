package queuejobs

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type FleetTemplateUpdateWorker struct {
	river.WorkerDefaults[store.FleetTemplateUpdateArgs]
	log        logrus.FieldLogger
	db         *gorm.DB
	fleetStore store.Fleet
	devStore   store.Device
}

func NewFleetTemplateUpdateWorker(log logrus.FieldLogger, db *gorm.DB, store store.Store) *FleetTemplateUpdateWorker {
	return &FleetTemplateUpdateWorker{
		log:        log,
		db:         db,
		fleetStore: store.Fleet(),
		devStore:   store.Device(),
	}
}

func (w *FleetTemplateUpdateWorker) Work(origCtx context.Context, job *river.Job[store.FleetTemplateUpdateArgs]) error {
	reqid.OverridePrefix("device-updater")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(origCtx, middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, w.log)

	log.Info("FleetTemplateUpdateWorker: %s/%s", job.Args.OrgID, job.Args.Name)

	orgId, err := uuid.Parse(job.Args.OrgID)
	if err != nil {
		return fmt.Errorf("invalid uuid passed: %v", err)
	}
	fleet, err := w.fleetStore.Get(ctx, orgId, job.Args.Name)
	if err != nil {
		return fmt.Errorf("failed fetching fleet: %v", err)
	}

	failureCount := 0
	for {
		devices, err := w.devStore.List(ctx, orgId, store.ListParams{Labels: fleet.Spec.Selector.MatchLabels, Limit: ItemsPerPage})
		if err != nil {
			return fmt.Errorf("failed fetching fleet: %v", err)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err = w.updateDeviceSpecAccordingToFleetTemplate(ctx, log, orgId, device, fleet)
			if err != nil {
				log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, *fleet.Metadata.Name, err)
				failureCount = failureCount + 1
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
	}

	if failureCount != 0 {
		return fmt.Errorf("failed to update %d devices", failureCount)
	}
	return nil
}

func (w *FleetTemplateUpdateWorker) updateDeviceSpecAccordingToFleetTemplate(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID, device *api.Device, fleet *api.Fleet) error {
	if fleet.Spec.Template.Metadata == nil {
		return nil
	}
	targetGeneration := *fleet.Spec.Template.Metadata.Generation
	if device.Metadata.Generation != nil && *device.Metadata.Generation == targetGeneration {
		// Nothing to do
		return nil
	}

	return w.devStore.UpdateDeviceSpec(ctx, orgId, *device.Metadata.Name, targetGeneration, fleet.Spec.Template.Spec)
}
