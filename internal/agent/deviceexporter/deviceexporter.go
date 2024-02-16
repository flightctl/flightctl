package deviceexporter

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

// NewManager creates a new device exporter manager.
func NewManager(tpm *tpm.TPM, executor executer.Executer) *Manager {
	exporters := []DeviceExporter{
		newSystemDExporter(executor),
		newContainerExporter(executor),
		newSystemInfoExporter(tpm),
	}

	return &Manager{
		exporters: exporters,
	}
}

type Manager struct {
	exporters    []DeviceExporter
	pollInterval time.Duration
	logPrefix    string

	mu           sync.Mutex
	deviceStatus v1alpha1.DeviceStatus
}

type DeviceExporter interface {
	GetStatus(context.Context) (interface{}, error)
}

func (c *Manager) Run(ctx context.Context) error {
	klog.Infof("%sstarting device exporter manager", c.logPrefix)
	defer klog.Infof("%sstopping device exporter manager", c.logPrefix)

	wait.PollInfiniteWithContext(ctx, c.pollInterval, func(ctx context.Context) (bool, error) {
		deviceStatus, err := c.getDeviceStatus(ctx)
		if err != nil {
			klog.Errorf("error getting device status: %v", err)
			return false, nil
		}
		c.mu.Lock()
		c.deviceStatus = deviceStatus
		c.mu.Unlock()
		return true, nil
	})

	return nil
}

func (c *Manager) getDeviceStatus(ctx context.Context) (v1alpha1.DeviceStatus, error) {
	deviceStatus := v1alpha1.DeviceStatus{}
	for _, exporter := range c.exporters {
		status, err := exporter.GetStatus(ctx)
		if err != nil {
			klog.Errorf("failed getting status from exporter: %v", err)
			continue
		}

		switch s := status.(type) {
		case []v1alpha1.ContainerStatus:
			deviceStatus.Containers = &s
		case *v1alpha1.DeviceSystemInfo:
			deviceStatus.SystemInfo = s
		case []v1alpha1.DeviceSystemdUnitStatus:
			deviceStatus.SystemdUnits = &s
		default:
			return v1alpha1.DeviceStatus{}, fmt.Errorf("unknown exporter type: %T", status)
		}
	}
	return deviceStatus, nil
}

func (c *Manager) GetStatus() v1alpha1.DeviceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deviceStatus
}
