package device_updater

import (
	"context"

	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type API interface {
	Test()
}

type DeviceUpdater struct {
	log        logrus.FieldLogger
	db         *gorm.DB
	fleetStore service.FleetStore
	devStore   service.DeviceStore
}

func NewDeviceUpdater(log logrus.FieldLogger, db *gorm.DB, store *store.Store) *DeviceUpdater {
	return &DeviceUpdater{
		log:        log,
		db:         db,
		fleetStore: store.GetFleetStore(),
		devStore:   store.GetDeviceStore(),
	}
}

func (d *DeviceUpdater) UpdateDevices() {
	reqid.OverridePrefix("device-updater")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, d.log)

	log.Info("Running DeviceUpdater")

	fleets, err := d.fleetStore.ListAllFleetsInternal()
	if err != nil {
		log.Errorf("failed to list fleets: %v", err)
		return
	}

	for _, fleet := range fleets {
		// If there is no template set in the fleet, then there is nothing to sync to the devices
		if fleet.Spec == nil || fleet.Spec.Data.Template.Metadata == nil {
			continue
		}

		devices, err := d.devStore.ListAllDevicesInternal(fleet.Spec.Data.Selector.MatchLabels)
		if err != nil {
			log.Errorf("failed to list devices for fleet %s: %v", fleet.Name, err)
		}

		for _, device := range devices {
			updated := d.updateDeviceSpecAccordingToFleetTemplate(log, &device, &fleet) //nolint:gosec

			if updated {
				err := d.devStore.UpdateDeviceInternal(&device) //nolint:gosec
				if err != nil {
					log.Errorf("failed to update target generation for device %s (fleet %s): %v", device.Name, fleet.Name, err)
				}
			}
		}
	}
}

func (d *DeviceUpdater) updateDeviceSpecAccordingToFleetTemplate(log logrus.FieldLogger, device *model.Device, fleet *model.Fleet) bool {
	if fleet.Spec.Data.Template.Metadata == nil {
		return false
	}
	targetGeneration := *fleet.Spec.Data.Template.Metadata.Generation
	if device.Resource.Generation != nil && device.Resource.Generation == &targetGeneration {
		// Nothing to do
		return false
	}

	device.Generation = &targetGeneration
	device.Spec = model.MakeJSONField(fleet.Spec.Data.Template.Spec)
	log.Infof("Updating device %s to generation %d", device.Name, *device.Generation)
	return true
}
