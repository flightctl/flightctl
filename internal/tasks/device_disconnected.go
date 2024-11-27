package tasks

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	// DeviceDisconnectedPollingInterval is the interval at which the device liveness task runs.
	DeviceDisconnectedPollingInterval = 2 * time.Minute
)

type DeviceDisconnected struct {
	log         logrus.FieldLogger
	deviceStore store.Device
}

func NewDeviceDisconnected(log logrus.FieldLogger, store store.Store) *DeviceDisconnected {
	return &DeviceDisconnected{
		log:         log,
		deviceStore: store.Device(),
	}
}

// Poll checks the status of devices and updates the status to unknown if the device has not reported in the last DeviceDisconnectedTimeout.
func (t *DeviceDisconnected) Poll() {
	t.log.Info("Running DeviceDisconnected Polling")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	statusInfoMessage := fmt.Sprintf("Did not check in for more than %d minutes", int(api.DeviceDisconnectedTimeout.Minutes()))
	// TODO: one thread per org?
	orgID := uuid.UUID{}
	// batch of 1000 devices
	listParams := store.ListParams{Limit: ItemsPerPage}
	for {
		devices, err := t.deviceStore.List(ctx, orgID, listParams)
		if err != nil {
			t.log.WithError(err).Error("failed to list devices")
			return
		}

		var batch []string
		for _, device := range devices.Items {
			if device.Status != nil && device.Status.Summary.Status != api.DeviceSummaryStatusUnknown {
				if device.Status.LastSeen.Add(api.DeviceDisconnectedTimeout).Before(time.Now()) {
					batch = append(batch, *device.Metadata.Name)
				}
			}
		}

		t.log.Infof("Updating %d devices to unknown status", len(batch))
		// TODO: This is MVP and needs to be properly evaluated for performance and race conditions
		if err := t.deviceStore.UpdateSummaryStatusBatch(ctx, orgID, batch, api.DeviceSummaryStatusUnknown, statusInfoMessage); err != nil {
			t.log.WithError(err).Error("failed to update device summary status")
			return
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				t.log.WithError(err).Error("failed to parse continuation for paging")
				return
			}
			listParams.Continue = cont
		}
	}
}
