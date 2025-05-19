package resource

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

type Controller struct {
	log     *log.PrefixLogger
	manager Manager
}

func NewController(
	log *log.PrefixLogger,
	manager Manager,
) *Controller {
	return &Controller{
		log:     log,
		manager: manager,
	}
}

func (c *Controller) Sync(ctx context.Context, desired *v1alpha1.DeviceSpec) error {
	c.log.Debug("Syncing device image")
	defer c.log.Debug("Finished syncing device image")

	if desired.Resources == nil {
		c.log.Debug("Device resources are nil")
		// Reset all resource alerts to default
		return c.manager.ResetAlertDefaults()
	}

	return c.ensureMonitors(desired.Resources)
}

func (c *Controller) ensureMonitors(monitors *[]v1alpha1.ResourceMonitor) error {
	for i := range *monitors {
		monitor := (*monitors)[i]
		monitorType, err := monitor.Discriminator()
		if err != nil {
			return err
		}
		updated, err := c.manager.Update(&monitor)
		if err != nil {
			return err
		}
		if updated {
			c.log.Infof("Updated monitor: %s", monitorType)
		}
	}
	return nil
}
