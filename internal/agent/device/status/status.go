package status

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	tpm *tpm.TPM,
	executer executer.Executer,
	log *log.PrefixLogger,
) *StatusManager {
	appManager := newAppManager()
	exporters := []Exporter{
		newSystemD(executer, appManager),
		newContainer(executer, appManager),
		newSystemInfo(tpm),
	}
	return &StatusManager{
		deviceName: deviceName,
		exporters:  exporters,
		appManager: newAppManager(),
		conditions: DefaultConditions(),
		log:        log,
	}
}

// Collector aggregates device status from various exporters.
type StatusManager struct {
	deviceName       string
	managementClient *client.Management
	exporters        []Exporter
	appManager       *AppManager
	log              *log.PrefixLogger
	conditions       *[]v1alpha1.Condition
}

type Exporter interface {
	Export(ctx context.Context, device *v1alpha1.DeviceStatus) error
	SetProperties(*v1alpha1.RenderedDeviceSpec)
}

type Collector interface {
	Get(context.Context) (*v1alpha1.DeviceStatus, error)
}

type Manager interface {
	Collector
	Update(context.Context, *v1alpha1.DeviceStatus) error
	SetClient(*client.Management)
	UpdateConditionError(ctx context.Context, reason string, err error) error
	UpdateCondition(ctx context.Context, conditionType v1alpha1.ConditionType, conditionStatus v1alpha1.ConditionStatus, reason, message string) error
	SetProperties(*v1alpha1.RenderedDeviceSpec)
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
	deviceStatus := v1alpha1.DeviceStatus{Conditions: &[]v1alpha1.Condition{}}
	for _, exporter := range m.exporters {
		err := exporter.Export(ctx, &deviceStatus)
		if err != nil {
			m.log.Errorf("failed getting status from exporter: %v", err)
			continue
		}
	}

	deviceStatus.Conditions = m.conditions
	deviceStatus.Fingerprint = &m.deviceName

	// TODO: this is a naive implementation, we should use a time based LRU cache
	// TODO: wip not complete

	// Flow
	// 1. Get all applications from the device status
	// 2. Get all applications from the app manager
	// 3. If an application is not in the device status, add it
	// 4. If an application is in the device status, update its state
	// 5. If an application is in the device status but not in the app manager, mark it as stopped
	// 6. Return the updated device status

	previous := make(map[string]struct{})
	for _, app := range *deviceStatus.Applications {
		previous[*app.Name] = struct{}{}
	}

	for name, app := range m.appManager.apps {
		if _, ok := previous[name]; !ok {
			// new app
			*deviceStatus.Applications = append(*deviceStatus.Applications, app)
		}
	}

	return &deviceStatus, nil
}

func (m *StatusManager) Update(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	// we keep our status conditions in memory, so don't stomp on it
	if status.Conditions == nil {
		return fmt.Errorf("status conditions not set")
	}

	// need a basic device object to update status
	device := v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: &m.deviceName,
		},
		Status: status,
	}

	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, device); err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}
	// update conditions
	m.conditions = status.Conditions
	return nil
}

func (m *StatusManager) UpdateCondition(
	ctx context.Context,
	conditionType v1alpha1.ConditionType,
	conditionStatus v1alpha1.ConditionStatus,
	reason,
	message string,
) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	status, err := m.Get(ctx)
	if err != nil {
		return err
	}

	if status.Conditions == nil {
		return fmt.Errorf("status conditions not set")
	}

	if SetProgressingCondition(status.Conditions, conditionType, conditionStatus, reason, message) {
		// log condition change
		m.log.Infof("Set progressing condition: %s", reason)
	}

	return m.Update(ctx, status)
}

func (m *StatusManager) UpdateConditionError(ctx context.Context, reason string, serr error) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	status, err := m.Get(ctx)
	if err != nil {
		return err
	}

	if status.Conditions == nil {
		return fmt.Errorf("status conditions not set")
	}

	if SetDegradedConditionByError(status.Conditions, reason, serr) {
		// log condition change
		m.log.Infof("Set degraded condition by error: %v", serr)
	}

	return m.Update(ctx, status)
}

func (m *StatusManager) SetProperties(spec *v1alpha1.RenderedDeviceSpec) {
	for _, exporter := range m.exporters {
		exporter.SetProperties(spec)
	}
}

// AppManager is responsible for managing and collecting the status of applications on a device.
type AppManager struct {
	apps map[string]v1alpha1.ApplicationStatus
}

// newAppManager creates a new AppManager.
func newAppManager() *AppManager {
	return &AppManager{
		apps: make(map[string]v1alpha1.ApplicationStatus),
	}
}

func (a *AppManager) ExportStatus(name string, status v1alpha1.ApplicationStatus) {
	a.apps[name] = status
}
