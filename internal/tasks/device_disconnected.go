package tasks

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// DeviceDisconnectedPollingInterval is the interval at which the device liveness task runs.
	DeviceDisconnectedPollingInterval = 2 * time.Minute
	DeviceDisconnectedTaskName        = "device-disconnected"
)

type DeviceDisconnected struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
}

func NewDeviceDisconnected(log logrus.FieldLogger, serviceHandler service.Service) *DeviceDisconnected {
	return &DeviceDisconnected{
		log:            log,
		serviceHandler: serviceHandler,
	}
}

// Poll checks the status of devices and updates the status to unknown if the device has not reported in the last DeviceDisconnectedTimeout.
func (t *DeviceDisconnected) Poll(ctx context.Context, orgID uuid.UUID) {
	t.log.Info("Running DeviceDisconnected Polling")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Calculate the cutoff time for disconnected devices
	cutoffTime := time.Now().UTC().Add(-api.DeviceDisconnectedTimeout)

	// List devices that match the disconnection criteria with pagination
	listParams := api.ListDevicesParams{
		Limit: lo.ToPtr(int32(ItemsPerPage)),
	}

	totalProcessed := 0
	for {
		// Check for context cancellation in long-running loops
		if ctx.Err() != nil {
			t.log.Warnf("Context cancelled during device disconnection processing, stopping early. Processed %d devices so far", totalProcessed)
			return
		}

		devices, status := t.serviceHandler.ListDisconnectedDevices(ctx, orgID, listParams, cutoffTime)
		if status.Code != 200 {
			t.log.Errorf("Failed to list devices: %s", status.Message)
			return
		}

		if len(devices.Items) == 0 {
			t.log.Debug("No devices found, stopping")
			break
		}

		t.log.Infof("Processing %d devices for disconnection status (page total: %d)", len(devices.Items), totalProcessed+len(devices.Items))

		for _, device := range devices.Items {
			// Check for context cancellation in long-running loops
			if ctx.Err() != nil {
				t.log.Warnf("Context cancelled during device processing, stopping early. Processed %d devices so far", totalProcessed)
				return
			}

			t.log.Debugf("Updating server-side device status for %s", *device.Metadata.Name)
			err := t.serviceHandler.UpdateServerSideDeviceStatus(ctx, orgID, *device.Metadata.Name)
			if err != nil {
				t.log.Errorf("Failed to update server-side device status for %s: %s", *device.Metadata.Name, err.Error())
				continue
			}

			t.log.Debugf("Successfully updated device %s to disconnected status", *device.Metadata.Name)
		}

		totalProcessed += len(devices.Items)

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	if totalProcessed > 0 {
		t.log.Infof("Completed processing %d devices for disconnection status", totalProcessed)
	}
}
