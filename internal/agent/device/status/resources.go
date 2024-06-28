package status

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
)

var _ Exporter = (*Resources)(nil)

type Resources struct {
	manager resource.Manager
}

func newResources(manager resource.Manager) *Resources {
	return &Resources{
		manager: manager,
	}
}

func (r *Resources) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	usage, err := r.manager.Usage()
	if err != nil {
		status.Resources.Disk = v1alpha1.DeviceResourceStatusUnknown
		return fmt.Errorf("getting resource usage: %w", err)
	}

	switch {
	case usage.FsUsage.IsAlert():
		status.Resources.Disk = v1alpha1.DeviceResourceStatusCritical
	case usage.FsUsage.IsWarn():
		status.Resources.Disk = v1alpha1.DeviceResourceStatusWarning
	default:
		status.Resources.Disk = v1alpha1.DeviceResourceStatusHealthy
	}

	return nil
}

func (r *Resources) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
