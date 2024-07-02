package status

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	resourceManager resource.Manager,
	executer executer.Executer,
	log *log.PrefixLogger,
) *StatusManager {
	exporters := []Exporter{
		newSystemD(executer),
		newContainer(executer),
		newSystemInfo(executer),
		newResources(log, resourceManager),
	}
	status := v1alpha1.NewDeviceStatus()
	return &StatusManager{
		deviceName: deviceName,
		exporters:  exporters,
		device: &v1alpha1.Device{
			Metadata: v1alpha1.ObjectMeta{
				Name: &deviceName,
			},
			Status: &status,
		},
		log: log,
	}
}

// Collector aggregates device status from various exporters.
type StatusManager struct {
	deviceName       string
	managementClient *client.Management
	exporters        []Exporter
	log              *log.PrefixLogger
	device           *v1alpha1.Device
}

type Exporter interface {
	Export(ctx context.Context, device *v1alpha1.DeviceStatus) error
	SetProperties(*v1alpha1.RenderedDeviceSpec)
}

type Collector interface {
	Get(context.Context) *v1alpha1.DeviceStatus
}

type Manager interface {
	Collector
	Sync(context.Context) error
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error)
	UpdateCondition(context.Context, v1alpha1.Condition) error
	SetClient(*client.Management)
	SetProperties(*v1alpha1.RenderedDeviceSpec)
}

func (m *StatusManager) SetClient(managementCLient *client.Management) {
	m.managementClient = managementCLient
}

func (m *StatusManager) Get(ctx context.Context) *v1alpha1.DeviceStatus {
	return m.device.Status
}

func (m *StatusManager) syncDeviceStatus(ctx context.Context) error {
	for _, exporter := range m.exporters {
		err := exporter.Export(ctx, m.device.Status)
		if err != nil {
			m.log.Errorf("failed getting status from exporter: %v", err)
			continue
		}
	}
	return nil
}

func (m *StatusManager) Sync(ctx context.Context) error {
	if err := m.syncDeviceStatus(ctx); err != nil {
		return fmt.Errorf("failed to sync device status: %w", err)
	}
	if m.managementClient == nil {
		return nil
	}
	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}
	return nil
}

func (m *StatusManager) UpdateCondition(ctx context.Context, condition v1alpha1.Condition) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	if condition.LastTransitionTime.IsZero() {
		condition.LastTransitionTime = time.Now()
	}

	m.device.Status.Conditions[string(condition.Type)] = condition

	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}

	return nil
}

func (m *StatusManager) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
	for _, exporter := range m.exporters {
		exporter.SetProperties(spec)
	}
}

type UpdateStatusFn func(status *v1alpha1.DeviceStatus) error

func (m *StatusManager) Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error) {
	for _, update := range updateFuncs {
		if err := update(m.device.Status); err != nil {
			return nil, err
		}
	}

	// TODO: handle retries
	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		return nil, fmt.Errorf("failed to update device status: %w", err)
	}

	return m.device.Status, nil
}

func SetDeviceSummary(summaryStatus v1alpha1.DeviceSummaryStatus) UpdateStatusFn {
	return func(status *v1alpha1.DeviceStatus) error {
		status.Summary.Status = summaryStatus.Status
		status.Summary.Info = summaryStatus.Info
		return nil
	}
}

func SetConfig(configStatus v1alpha1.DeviceConfigStatus) UpdateStatusFn {
	return func(status *v1alpha1.DeviceStatus) error {
		status.Config.RenderedVersion = configStatus.RenderedVersion
		return nil
	}
}

func SetOSImage(osStatus v1alpha1.DeviceOSStatus) UpdateStatusFn {
	return func(status *v1alpha1.DeviceStatus) error {
		status.Os.Image = osStatus.Image
		return nil
	}
}
