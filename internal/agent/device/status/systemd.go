package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
)

var _ Exporter = (*SystemD)(nil)

// SystemD collects systemd unit status
type SystemD struct {
	manager systemd.Manager
}

func newSystemD(manager systemd.Manager) *SystemD {
	return &SystemD{
		manager: manager,
	}
}

func (c *SystemD) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	units, err := c.manager.Status(ctx)
	if err != nil {
		return err
	}

	status.Applications = append(status.Applications, units...)

	return nil
}
