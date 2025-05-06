package server

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/cloudevents/util"
	"github.com/flightctl/flightctl/internal/store"
)

// DeviceController monitors device rendering. Upon completing a device's rendering with flightctl,
// it publishes the device specs to the agent through gRPC.
//
// TODO Currently, this controller polls the devices from database to check if the devices is
// updated. Next, we will consider
//   - the use flightctl asynchronous tasks to reimplement this. after the devices was rendered with
//     `DeviceRenderTask`, we publish a task to notify this controller to publish the device update.
//   - Or, leverage the PostgreSQL LISTEN/NOTIFY mechanism to send an event to notify this controller.
type DeviceController struct {
	log logrus.FieldLogger

	st  store.Store
	svc *DeviceService

	devices map[string]string
}

// NewDeviceController returns a NewDeviceController
func NewDeviceController(log logrus.FieldLogger, st store.Store, svc *DeviceService) *DeviceController {
	return &DeviceController{
		log:     log,
		st:      st,
		svc:     svc,
		devices: make(map[string]string),
	}
}

func (c *DeviceController) Run(ctx context.Context) {
	c.log.Info("Starting to watch devices update with cloudevents ...")
	go wait.JitterUntil(func() {
		devices, err := c.st.Device().List(ctx, store.NullOrgId, store.ListParams{})
		if err != nil {
			c.log.Errorf("failed to list devices, %v", err)
		}

		for _, d := range devices.Items {
			if d.Metadata.Annotations == nil {
				continue
			}

			currentRenderedVersion, ok := (*d.Metadata.Annotations)[v1alpha1.DeviceAnnotationRenderedVersion]
			if !ok {
				// devices without a rendered version cannot be published
				continue
			}

			lastRenderedVersion, ok := c.devices[*d.Metadata.Name]
			if !ok {
				c.devices[*d.Metadata.Name] = currentRenderedVersion
				c.log.Infof("publish device %s (%s,%s)", *d.Metadata.Name, lastRenderedVersion, currentRenderedVersion)
				if err := c.svc.Publish(ctx, *d.Metadata.Name); err != nil {
					c.log.Errorf("failed to publish device %s, %v", *d.Metadata.Name, err)
				}
				continue
			}

			if !util.CompareDeviceVersion(lastRenderedVersion, currentRenderedVersion) {
				c.log.Debugf("ignore the device %s update (%s, %s)",
					*d.Metadata.Name, lastRenderedVersion, currentRenderedVersion)
				continue
			}

			c.log.Infof("publish device %s (%s, %s)", *d.Metadata.Name, lastRenderedVersion, currentRenderedVersion)
			if err := c.svc.Publish(ctx, *d.Metadata.Name); err != nil {
				c.log.Errorf("failed to publish device %s, %v", *d.Metadata.Name, err)
			}
			c.devices[*d.Metadata.Name] = currentRenderedVersion
		}
	}, 10*time.Second, 0.25, true, ctx.Done())
}
