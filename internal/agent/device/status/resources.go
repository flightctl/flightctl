package status

import (
	"context"
	"errors"
	"fmt"

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
	errs := []error{}

	// disk
	diskStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.DiskUsage)
	if err != nil {
		errs = append(errs, fmt.Errorf("disk: %w", err))
	}
	status.Resources.Disk = diskStatus

	// cpu
	cpuStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.CPUUsage)
	if err != nil {
		errs = append(errs, fmt.Errorf("cpu: %w", err))
	}
	status.Resources.Cpu = cpuStatus

	// memory
	memoryStatus, err := resource.GetHighestSeverityResourceStatusFromAlerts(alerts.MemoryUsage)
	if err != nil {
		errs = append(errs, fmt.Errorf("memory: %w", err))
	}
	status.Resources.Memory = memoryStatus

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (r *Resources) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
