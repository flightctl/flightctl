package device_updater

import (
	"context"
	"fmt"
	"time"

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
	fleetStore service.FleetStoreInterface
	devStore   service.DeviceStoreInterface
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
		log.Errorf("failed to list fleets: %w", err)
		return
	}

	for _, fleet := range fleets {
		// If there is no template set in the fleet, then there is nothing to sync to the devices
		if fleet.Spec == nil || fleet.Spec.Data.Template.Metadata == nil {
			continue
		}

		devices, err := d.devStore.ListAllDevicesInternal(fleet.Spec.Data.Selector.MatchLabels)
		if err != nil {
			log.Errorf("failed to list devices for fleet %s: %w", fleet.Name, err)
		}

		for _, device := range devices {
			d.updateDeviceSpecAccordingToFleetTemplate(log, &device, &fleet)
			d.updateDeviceHeartbeatConditions(log, &device, &fleet)
			err := d.devStore.UpdateDeviceInternal(&device)
			if err != nil {
				log.Errorf("Failed to update target generation for device %s (fleet %s): %w", device.Name, fleet.Name, err)
			}
		}
	}
}

func (d *DeviceUpdater) updateDeviceSpecAccordingToFleetTemplate(log logrus.FieldLogger, device *model.Device, fleet *model.Fleet) {
	targetGeneration := *fleet.Spec.Data.Template.Metadata.Generation
	if device.Resource.Generation != nil && device.Resource.Generation == &targetGeneration {
		// Nothing to do
		return
	}

	device.Generation = &targetGeneration
	device.Spec = model.MakeJSONField(fleet.Spec.Data.Template.Spec)
	log.Infof("Updating device %s to generation %d", device.Name, *device.Generation)
}

func (d *DeviceUpdater) updateDeviceHeartbeatConditions(log logrus.FieldLogger, device *model.Device, fleet *model.Fleet) {
	// If the device has never updated its status, there's nothing to do
	if device.Status.Data.UpdatedAt == nil {
		return
	}

	warningTime := fleet.Spec.Data.DeviceConditions.HeartbeatElapsedTimeWarning
	errorTime := fleet.Spec.Data.DeviceConditions.HeartbeatElapsedTimeError
	now := util.TimeStampStringPtr()

	if device.Status.Data.Conditions == nil {
		device.Status.Data.Conditions = &[]api.DeviceCondition{
			{
				Type:               string(api.HeartbeatElapsedTimeWarning),
				Status:             api.False,
				LastHeartbeatTime:  now,
				LastTransitionTime: now,
			},
			{
				Type:               string(api.HeartbeatElapsedTimeError),
				Status:             api.False,
				LastHeartbeatTime:  now,
				LastTransitionTime: now,
			},
		}
	}

	if warningTime == nil {
		d.setCondition(device, string(api.HeartbeatElapsedTimeWarning), api.False, now, "No threshold set", "No threshold set")
	} else {
		duration, _ := time.ParseDuration(*warningTime)
		threshold := time.Now().Add(duration * -1)
		d.setHeartbeatCondition(device, string(api.HeartbeatElapsedTimeWarning), now, threshold)
	}

	if errorTime == nil {
		d.setCondition(device, string(api.HeartbeatElapsedTimeError), api.False, now, "No threshold set", "No threshold set")
	} else {
		duration, _ := time.ParseDuration(*errorTime)
		threshold := time.Now().Add(duration * -1)
		d.setHeartbeatCondition(device, string(api.HeartbeatElapsedTimeError), now, threshold)
	}
}

func (d *DeviceUpdater) setHeartbeatCondition(device *model.Device, condType string, now *string, threshold time.Time) {
	deviceUpdatedAt := util.ParseTimeStampIgnoreErrors(*device.Status.Data.UpdatedAt)

	if deviceUpdatedAt.Before(threshold) {
		d.setCondition(device, condType, api.True, now, "Threshold exceeded", fmt.Sprintf("Threshold exceeded by %s", threshold.Sub(deviceUpdatedAt).Truncate(time.Second).String()))
	} else {
		d.setCondition(device, condType, api.False, now, "Threshold not exceeded", "")
	}
}

func (d *DeviceUpdater) setCondition(device *model.Device, condType string, status api.ConditionStatus, now *string, reason, message string) {
	var condIndex int
	for i, cond := range *device.Status.Data.Conditions {
		if cond.Type == condType {
			condIndex = i
			break
		}
	}

	if (*device.Status.Data.Conditions)[condIndex].Status != status {
		(*device.Status.Data.Conditions)[condIndex].LastTransitionTime = now
		(*device.Status.Data.Conditions)[condIndex].Status = status
	}
	(*device.Status.Data.Conditions)[condIndex].LastHeartbeatTime = now
	(*device.Status.Data.Conditions)[condIndex].Reason = &reason
	(*device.Status.Data.Conditions)[condIndex].Message = &message
}
