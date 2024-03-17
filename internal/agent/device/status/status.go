package status

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"k8s.io/klog/v2"
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	tpm *tpm.TPM,
	executor executer.Executer,
) *StatusManager {
	exporters := []Exporter{
		newSystemD(executor),
		newContainer(executor),
		newSystemInfo(tpm),
	}
	return &StatusManager{
		deviceName: deviceName,
		exporters:  exporters,
	}
}

// Collector aggregates device status from various exporters.
type StatusManager struct {
	deviceName       string
	managementClient *client.Management
	exporters        []Exporter
}

type Exporter interface {
	Export(ctx context.Context, device *v1alpha1.DeviceStatus) error
}

type Collector interface {
	Get(context.Context) (*v1alpha1.DeviceStatus, error)
}

type Manager interface {
	Collector
	Update(context.Context, *v1alpha1.DeviceStatus) error
}

func (m *StatusManager) SetClient(managementCLient *client.Management) {
	m.managementClient = managementCLient
}

func (m *StatusManager) Get(ctx context.Context) (*v1alpha1.DeviceStatus, error) {
	deviceStatus, err := m.aggregateDeviceStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get device status: %w", err)
	}
	return deviceStatus, nil
}

func (m *StatusManager) aggregateDeviceStatus(ctx context.Context) (*v1alpha1.DeviceStatus, error) {
	deviceStatus := v1alpha1.DeviceStatus{}
	for _, exporter := range m.exporters {
		err := exporter.Export(ctx, &deviceStatus)
		if err != nil {
			klog.Errorf("failed getting status from exporter: %v", err)
			continue
		}
	}

	return &deviceStatus, nil
}

func (m *StatusManager) Update(ctx context.Context, device *v1alpha1.DeviceStatus) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	buf := &bytes.Buffer{}
	err := json.NewEncoder(buf).Encode(device)
	if err != nil {
		return fmt.Errorf("failed to encode device: %w", err)
	}
	return m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, buf)
}
