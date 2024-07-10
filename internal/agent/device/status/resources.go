package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Exporter = (*Resources)(nil)

type Resources struct {
	manager resource.Manager
	log     *log.PrefixLogger
}

func newResources(log *log.PrefixLogger, manager resource.Manager) *Resources {
	return &Resources{
		manager: manager,
		log:     log,
	}
}

func (r *Resources) Export(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	alerts := r.manager.Alerts()

	// disk
	diskStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.DiskUsage)
	if err != nil {
		// TODO: add error to status
		r.log.Errorf("Error getting disk usage status: %v", err)
	}
	status.Resources.Disk = diskStatus

	// cpu
	cpuStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.CPUUsage)
	if err != nil {
		r.log.Errorf("Error getting cpu usage status: %v", err)
	}
	status.Resources.Cpu = cpuStatus

	// memory
	memoryStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.MemoryUsage)
	if err != nil {
		r.log.Errorf("Error getting memory usage status: %v", err)
	}
	status.Resources.Memory = memoryStatus

	return nil
}

func (r *Resources) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
