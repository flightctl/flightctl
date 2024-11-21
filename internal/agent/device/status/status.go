package status

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	resourceManager resource.Manager,
	hookManager hook.Manager,
	applicationManager applications.Manager,
	systemdManager systemd.Manager,
	executer executer.Executer,
	log *log.PrefixLogger,
) *StatusManager {
	exporters := newExporters(resourceManager, hookManager, applicationManager, systemdManager, executer, log)
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
	managementClient client.Management
	exporters        []Exporter
	log              *log.PrefixLogger
	device           *v1alpha1.Device
}

type Exporter interface {
	// Export collects status information and updates the device status.
	Export(ctx context.Context, device *v1alpha1.DeviceStatus) error
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
	// Update updates the device status with the given update functions.
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error)
	// UpdateCondition updates the device status with the given condition.
	UpdateCondition(context.Context, v1alpha1.Condition) error
	// SetClient sets the management client for the status manager.
	SetClient(client.Management)
}

type UpdateState string

const (
	// The agent is validating the desired device spec and downloading
	// dependencies. No changes have been made to the device's configuration
	// yet.
	UpdateStatePreparing UpdateState = "Preparing"
	//  The agent has validated the desired spec, downloaded all dependencies,
	//  and is ready to update. No changes have been made to the device's
	//  configuration yet.
	UpdateStateReadyToUpdate UpdateState = "ReadyToUpdate"
	// The agent has started the update transaction and is writing the update to
	// disk.
	UpdateStateApplyingUpdate UpdateState = "ApplyingUpdate"
	// The agent initiated a reboot required to activate the new OS image and configuration.
	UpdateStateRebooting UpdateState = "Rebooting"
	// The agent has successfully completed the update and the device is
	// conforming to its device spec. Note that the device's update status may
	// still be reported as `OutOfDate` if the device spec is not yet at the
	// same version as the fleet's device template
	UpdateStateUpdated UpdateState = "Updated"
	// The agent has canceled the update because the desired spec was reverted
	// to the current spec before the update process started.
	UpdateStateCanceled UpdateState = "Canceled"
	// The agent failed to apply the desired spec and will not retry. The
	// device's OS image and configuration have been rolled back to the
	// pre-update version and have been activated
	UpdateStateError UpdateState = "Error"
	// The agent failed to apply the desired spec and will retry. The device's
	// OS image and configuration have been rolled back to the pre-update
	// version and have been activated.
	UpdateStateRetrying UpdateState = "Retrying"
)

func (m *StatusManager) SetClient(managementClient client.Management) {
	m.managementClient = managementClient
}

func (m *StatusManager) Get(ctx context.Context) *v1alpha1.DeviceStatus {
	return m.device.Status
}

func (m *StatusManager) reset() {
	m.device.Status.Applications = m.device.Status.Applications[:0]
}

func (m *StatusManager) Collect(ctx context.Context) error {
	m.reset()
	errs := []error{}
	for _, exporter := range m.exporters {
		err := exporter.Export(ctx, m.device.Status)
		if err != nil {
			errs = append(errs, err)
			continue
		}
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
		return fmt.Errorf("failed to update device status: %w", err)
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
