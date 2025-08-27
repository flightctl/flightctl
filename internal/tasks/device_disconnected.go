package tasks

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
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
func (t *DeviceDisconnected) Poll(ctx context.Context) {
	t.log.Info("Running DeviceDisconnected Polling")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Calculate the cutoff time for disconnected devices
	cutoffTime := time.Now().Add(-api.DeviceDisconnectedTimeout)

	// Create a field selector to only get devices that haven't been seen for more than DeviceDisconnectedTimeout
	// and don't already have "Unknown" status to avoid reprocessing the same devices
	fieldSelectorStr := fmt.Sprintf("status.lastSeen<%s,status.summary.status!=Unknown", cutoffTime.Format(time.RFC3339))

	// List devices that match the disconnection criteria with pagination
	listParams := api.ListDevicesParams{
		FieldSelector: &fieldSelectorStr,
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
	}

	totalProcessed := 0
	for {
		// Check for context cancellation in long-running loops
		if ctx.Err() != nil {
			t.log.Warnf("Context cancelled during device disconnection processing, stopping early. Processed %d devices so far", totalProcessed)
			return
		}

		devices, status := t.serviceHandler.ListDevices(ctx, listParams, nil)
		if status.Code != 200 {
			t.log.Errorf("Failed to list devices: %s", status.Message)
			return
		}

		if len(devices.Items) == 0 {
			break
		}

		t.log.Infof("Processing %d devices for disconnection status (page total: %d)", len(devices.Items), totalProcessed+len(devices.Items))

		for _, device := range devices.Items {
			// Check for context cancellation in long-running loops
			if ctx.Err() != nil {
				t.log.Warnf("Context cancelled during device processing, stopping early. Processed %d devices so far", totalProcessed)
				return
			}

			_, status := t.serviceHandler.ReplaceDeviceStatus(ctx, *device.Metadata.Name, device)
			if status.Code != 200 {
				t.log.Errorf("Failed to replace device status for %s: %s", *device.Metadata.Name, status.Message)
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
