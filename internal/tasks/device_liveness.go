package tasks

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const (
	// TODO: make configurable
	// DeviceLivenessTimeout is the duration after which a device is considered to be not reporting and set to unknown status.
	DeviceLivenessTimeout = 5 * time.Minute
	// DeviceLivenessPollingInterval is the interval at which the device liveness task runs.
	DeviceLivenessPollingInterval = 2 * time.Minute
)

type DeviceLiveness struct {
	log         logrus.FieldLogger
	deviceStore store.Device
}

func NewDeviceLiveness(log logrus.FieldLogger, store store.Store) *DeviceLiveness {
	return &DeviceLiveness{
		log:         log,
		deviceStore: store.Device(),
	}
}

// Poll checks the status of devices and updates the status to unknown if the device has not reported in the last DeviceLivenessTimeout.
func (t *DeviceLiveness) Poll() {
	t.log.Info("Running DeviceLiveness Polling")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

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
			if device.Status != nil && device.Status.Summary.Status != v1alpha1.DeviceSummaryStatusUnknown {
				if device.Status.UpdatedAt.Add(DeviceLivenessTimeout).Before(time.Now()) {
					batch = append(batch, *device.Metadata.Name)
				}
			}
		}

		t.log.Infof("Updating %d devices to unknown status", len(batch))
		if err := t.deviceStore.UpdateSummaryStatusBatch(ctx, orgID, batch, v1alpha1.DeviceSummaryStatusUnknown); err != nil {
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
