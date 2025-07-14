package tasks

import (
	"context"
	"fmt"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// DeviceDisconnectedPollingInterval is the interval at which the device liveness task runs.
	DeviceDisconnectedPollingInterval = 2 * time.Minute
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

	statusInfoMessage := fmt.Sprintf("Did not check in for more than %d minutes", int(api.DeviceDisconnectedTimeout.Minutes()))

	// Calculate the cutoff time for disconnected devices
	cutoffTime := time.Now().Add(-api.DeviceDisconnectedTimeout)

	// Create a field selector to only get devices that haven't been seen for more than DeviceDisconnectedTimeout
	// and don't already have "Unknown" status to avoid reprocessing the same devices
	fieldSelector := fmt.Sprintf("status.lastSeen<%s,status.summary.status!=Unknown", cutoffTime.Format(time.RFC3339))

	listParams := api.ListDevicesParams{
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
		FieldSelector: &fieldSelector,
	}

	for {
		devices, status := t.serviceHandler.ListDevices(ctx, listParams, nil)
		if status.Code != http.StatusOK {
			t.log.WithError(service.ApiStatusToErr(status)).Error("failed to list devices")
			return
		}

		var batch []string
		for _, device := range devices.Items {
			// Update the device status on the service side
			changed := t.serviceHandler.UpdateServiceSideDeviceStatus(ctx, device)
			if changed {
				batch = append(batch, *device.Metadata.Name)
			}
		}

		if len(batch) > 0 {
			t.log.Infof("Updating %d devices to unknown status", len(batch))
			// TODO: This is MVP and needs to be properly evaluated for performance and race conditions
			if status = t.serviceHandler.UpdateDeviceSummaryStatusBatch(ctx, batch, api.DeviceSummaryStatusUnknown, statusInfoMessage); status.Code != http.StatusOK {
				t.log.WithError(service.ApiStatusToErr(status)).Error("failed to update device summary status")
				return
			}
		} else {
			t.log.Debug("No disconnected devices found")
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}
}
