package device_updater

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
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

func NewDeviceUpdater(log logrus.FieldLogger, db *gorm.DB, stores *store.Store) *DeviceUpdater {
	return &DeviceUpdater{
		log:        log,
		db:         db,
		fleetStore: stores.GetFleetStore(),
		devStore:   stores.GetDeviceStore(),
	}
}

func (d *DeviceUpdater) UpdateDevices() {
	reqid.OverridePrefix("device-updater")
	requestID := reqid.NextRequestID()
	ctx := context.WithValue(context.Background(), middleware.RequestIDKey, requestID)
	log := log.WithReqIDFromCtx(ctx, d.log)

	log.Info("Running DeviceUpdater")

	fleets, err := d.fleetStore.ListIgnoreOrg()
	if err != nil {
		log.Errorf("failed to list fleets: %v", err)
		return
	}

	for fleetIndex := range fleets {
		orgID := fleets[fleetIndex].OrgID
		// If there is no template set in the fleet, then there is nothing to sync to the devices
		if fleets[fleetIndex].Spec == nil || fleets[fleetIndex].Spec.Data.Template.Metadata == nil {
			continue
		}

		listParams := service.ListParams{
			Labels: fleets[fleetIndex].Spec.Data.Selector.MatchLabels,
			Limit:  service.MaxRecordsPerListRequest, //TODO: Handle paging
		}
		devices, err := d.devStore.List(ctx, orgID, listParams)
		if err != nil {
			log.Errorf("failed to list devices for fleet %s: %v", fleets[fleetIndex].Name, err)
		}

		for devIndex := range devices.Items {
			dev := devices.Items[devIndex]
			if dev.Metadata.Owner == nil || *dev.Metadata.Owner == "" {
				dev.Metadata.Owner = util.SetResourceOwner(model.FleetKind, fleets[fleetIndex].Name)
				log.Infof("Setting previously unset fleet of device %s to %s", *dev.Metadata.Name, *dev.Metadata.Owner)
			} else if *dev.Metadata.Owner != fleets[fleetIndex].Name {
				// The fleet for this device changed
				err = d.handleOwningFleetChanged(ctx, log, &dev, &fleets[fleetIndex])
				if err != nil {
					log.Errorf("failed to handle fleet update for device %s: %v", *dev.Metadata.Name, err)
				}
			}

			updated := d.updateDeviceSpecAccordingToFleetTemplate(log, &dev, &fleets[fleetIndex])

			if updated {
				_, _, err := d.devStore.CreateOrUpdate(ctx, orgID, &dev)
				if err != nil {
					log.Errorf("failed to update device %s (fleet %s): %v", *dev.Metadata.Name, fleets[fleetIndex].Name, err)
				}
			}
		}

		// Handle devices that don't match the fleet's label selector but still have the owner
		err = d.handleOwningFleetOrphaned(ctx, log, &fleets[fleetIndex])
		if err != nil {
			log.Errorf("failed to update orphaned devices for fleet %s: %v", fleets[fleetIndex].Name, err)
		}
	}
}

/*
Handle the case where the device matches a fleet that isn't its owner. This
can happen if the selector changed or the device's labels changed. We need to
check that the current owner fleet doesn't still match the device before we
update the device's owner, because that means the descriptors of two fleets
match the device.
*/
func (d *DeviceUpdater) handleOwningFleetChanged(ctx context.Context, log logrus.FieldLogger, device *api.Device, fleet *model.Fleet) error {
	// "fleet" is potentially the new owner of "device" because, but we first need
	// to make sure that the label selectors of both the current fleet and the new
	// fleet aren't a match for this device.
	currentOwningFleet, err := d.fleetStore.Get(ctx, fleet.Resource.OrgID, *device.Metadata.Owner)
	if err != nil && err != gorm.ErrRecordNotFound {
		return err
	}
	if currentOwningFleet != nil && util.LabelsMatchLabelSelector(*device.Metadata.Labels, currentOwningFleet.Spec.Selector.MatchLabels) {
		d.log.Warnf("Device %s matches more than one fleet (%s and %s)", *device.Metadata.Name, *device.Metadata.Owner, fleet.Name)
		// Oh no, a device with 2 owners! How do we indicate that?
	} else {
		log.Infof("Updating fleet of device %s from %s to %s", *device.Metadata.Name, *device.Metadata.Owner, fleet.Name)
		device.Metadata.Owner = util.SetResourceOwner(model.FleetKind, fleet.Name)
	}

	return nil
}

/*
Handle devices that don't match their owner fleet's selector.
*/
func (d *DeviceUpdater) handleOwningFleetOrphaned(ctx context.Context, log logrus.FieldLogger, fleet *model.Fleet) error {
	// Perform a query of devices that don't match the label selector but still have the owner
	listParams := service.ListParams{
		Labels:       fleet.Spec.Data.Selector.MatchLabels,
		InvertLabels: util.BoolToPtr(true),
		Owner:        util.SetResourceOwner(model.FleetKind, fleet.Name),
		Limit:        service.MaxRecordsPerListRequest, //TODO: Handle paging
	}
	devices, err := d.devStore.List(ctx, fleet.OrgID, listParams)
	if err != nil {
		return fmt.Errorf("failed to list devices that no longer belong to fleet %s: %v", fleet.Name, err)
	}

	for devIndex := range devices.Items {
		dev := devices.Items[devIndex]
		d.log.Infof("Unsetting fleet for device %s (was %s)", *dev.Metadata.Name, *dev.Metadata.Owner)
		dev.Metadata.Owner = util.StrToPtr("")
		_, _, err := d.devStore.CreateOrUpdate(ctx, fleet.OrgID, &dev)
		if err != nil {
			return fmt.Errorf("failed to update device without fleet %s: %v", *dev.Metadata.Name, err)
		}
	}
	return nil
}

func (d *DeviceUpdater) updateDeviceSpecAccordingToFleetTemplate(log logrus.FieldLogger, device *api.Device, fleet *model.Fleet) bool {
	if fleet.Spec.Data.Template.Metadata == nil {
		return false
	}
	targetGeneration := *fleet.Spec.Data.Template.Metadata.Generation
	if device.Metadata.Generation != nil && device.Metadata.Generation == &targetGeneration {
		// Nothing to do
		return false
	}

	device.Metadata.Generation = &targetGeneration
	device.Spec = fleet.Spec.Data.Template.Spec
	log.Infof("Updating device %s to generation %d", *device.Metadata.Name, *device.Metadata.Generation)
	return true
}
