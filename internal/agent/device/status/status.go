package status

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"k8s.io/klog/v2"
)

var _ Getter = (*Collector)(nil)

// NewCollector creates a new device status collector.
func NewCollector(
	tpm *tpm.TPM,
	executor executer.Executer,
) *Collector {
	exporters := []Exporter{
		newSystemD(executor),
		newContainer(executor),
		newSystemInfo(tpm),
	}

	return &Collector{
		exporters: exporters,
	}
}

// Collector aggregates device status from various exporters.
type Collector struct {
	exporters []Exporter
}

type Exporter interface {
	Export(ctx context.Context, device *v1alpha1.DeviceStatus) error
}

type Getter interface {
	Get(context.Context) (v1alpha1.DeviceStatus, error)
}

func (c *Collector) Get(ctx context.Context) (v1alpha1.DeviceStatus, error) {
	deviceStatus, err := c.aggregateDeviceStatus(ctx)
	if err != nil {
		return v1alpha1.DeviceStatus{}, fmt.Errorf("failed to get device status: %w", err)
	}
	return deviceStatus, nil
}

func (c *Collector) aggregateDeviceStatus(ctx context.Context) (v1alpha1.DeviceStatus, error) {
	deviceStatus := v1alpha1.DeviceStatus{}
	for _, exporter := range c.exporters {
		err := exporter.Export(ctx, &deviceStatus)
		if err != nil {
			klog.Errorf("failed getting status from exporter: %v", err)
			continue
		}
	}

	return deviceStatus, nil
}
