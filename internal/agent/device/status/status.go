package status

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	log *log.PrefixLogger,
) *StatusManager {
	status := v1alpha1.NewDeviceStatus()
	// remove the DeviceSpecValid condition because it is not owned by the agent.
	_ = v1alpha1.RemoveStatusCondition(&status.Conditions, v1alpha1.DeviceSpecValid)

	return &StatusManager{
		deviceName: deviceName,
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
	managementClient client.Management
	exporters        []Exporter
	log              *log.PrefixLogger
	device           *v1alpha1.Device
}

type Collector interface {
	// Get returns the device status and is safe to call without a management client.
	Get(context.Context) *v1alpha1.DeviceStatus
}

type Manager interface {
	Collector
	// Sync collects status information from all exporters and updates the device status.
	Sync(context.Context) error
	// Collect gathers status information from all exporters and is safe to call without a management client.
	Collect(context.Context) error
	// RegisterStatusExporterFn registers a function to be called when collecting status.
	RegisterStatusExporterFn(fn Exporter)
	// Update updates the device status with the given update functions.
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error)
	// UpdateCondition updates the device status with the given condition.
	UpdateCondition(context.Context, v1alpha1.Condition) error
	// SetClient sets the management client for the status manager.
	SetClient(client.Management)
}

type Exporter interface {
	Status(context.Context, *v1alpha1.DeviceStatus) error
}

func (m *StatusManager) SetClient(managementClient client.Management) {
	m.managementClient = managementClient
}

func (m *StatusManager) Get(ctx context.Context) *v1alpha1.DeviceStatus {
	return m.device.Status
}

func (m *StatusManager) reset() {
	m.device.Status.Applications = m.device.Status.Applications[:0]
}

func (m *StatusManager) RegisterStatusExporterFn(fn Exporter) {
	m.exporters = append(m.exporters, fn)
}

func (m *StatusManager) Collect(ctx context.Context) error {
	m.reset()
	errs := []error{}
	for _, export := range m.exporters {
		err := export.Status(ctx, m.device.Status)
		if err != nil {
			errs = append(errs, err)
			continue
		}
	}

	if err := systemInfoStatus(ctx, m.device.Status); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (m *StatusManager) Sync(ctx context.Context) error {
	if err := m.Collect(ctx); err != nil {
		return err
	}
	if m.managementClient == nil {
		m.log.Warn("management client not set")
		return nil
	}
	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		m.log.Warnf("Failed to update device status: %v", err)
	}
	return nil
}

func (m *StatusManager) UpdateCondition(ctx context.Context, condition v1alpha1.Condition) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	changed := v1alpha1.SetStatusCondition(&m.device.Status.Conditions, condition)
	if !changed {
		return nil
	}

	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}
	return nil
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
		status.Os.ImageDigest = osStatus.ImageDigest
		return nil
	}
}
