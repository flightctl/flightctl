package status

import (
	"context"
	"errors"

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
	diskStatus, alertMsg := resource.GetHighestSeverityResourceStatusFromAlerts(resource.DiskMonitorType, alerts.DiskUsage)
	if alertMsg != "" {
		errs = append(errs, errors.New(alertMsg))
	}
	status.Resources.Disk = diskStatus

	// cpu
	cpuStatus, alertMsg := resource.GetHighestSeverityResourceStatusFromAlerts(resource.CPUMonitorType, alerts.CPUUsage)
	if alertMsg != "" {
		errs = append(errs, errors.New(alertMsg))
	}
	status.Resources.Cpu = cpuStatus

	// memory
	memoryStatus, alertMsg := resource.GetHighestSeverityResourceStatusFromAlerts(resource.MemoryMonitorType, alerts.MemoryUsage)
	if alertMsg != "" {
		errs = append(errs, errors.New(alertMsg))
	}
	status.Resources.Memory = memoryStatus

	// the alertMsg is a message that gets bubbled up to the summary.info status field
	// if an alert is present.  these messages are not errors specifically but
	// for now the presence of an error sets the device status to degraded.
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (r *Resources) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
