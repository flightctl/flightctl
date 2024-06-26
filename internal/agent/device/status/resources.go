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
	resource := r.manager.Usage()

	// disk
	diskStatus, err := diskUsageStatus(resource.DiskUsage)
	if err != nil {
		// TODO: add error to status
		r.log.Errorf("Error getting disk usage status: %v", err)
	}
	status.Resources.Disk = diskStatus

	// cpu
	cpuStatus, err := cpuUsageStatus(resource.CPUUsage)
	if err != nil {
		r.log.Errorf("Error getting cpu usage status: %v", err)
	}
	status.Resources.Cpu = cpuStatus

	return nil
}

// TODO: make generic
func diskUsageStatus(usage *resource.DiskUsage) (v1alpha1.DeviceResourceStatusType, error) {
	switch {
	case usage.Error() != nil:
		return v1alpha1.DeviceResourceStatusError, usage.Error()
	case usage.IsAlert():
		return v1alpha1.DeviceResourceStatusCritical, nil
	case usage.IsWarn():
		return v1alpha1.DeviceResourceStatusWarning, nil
	default:
		return v1alpha1.DeviceResourceStatusHealthy, nil
	}
}

func cpuUsageStatus(usage *resource.CPUUsage) (v1alpha1.DeviceResourceStatusType, error) {
	switch {
	case usage.Error() != nil:
		return v1alpha1.DeviceResourceStatusError, usage.Error()
	case usage.IsAlert():
		return v1alpha1.DeviceResourceStatusCritical, nil
	case usage.IsWarn():
		return v1alpha1.DeviceResourceStatusWarning, nil
	default:
		return v1alpha1.DeviceResourceStatusHealthy, nil
	}
}

func (r *Resources) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
}
