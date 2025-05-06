package status

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	MaxMessageLength = 175
)

var _ Manager = (*StatusManager)(nil)

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	log *log.PrefixLogger,
) *StatusManager {
	status := v1alpha1.NewDeviceStatus()
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
	mu               sync.Mutex
	deviceName       string
	managementClient client.Management
	exporters        []Exporter
	device           *v1alpha1.Device

	log *log.PrefixLogger
}

type Exporter interface {
	// Status collects status information and updates the device status.
	Status(context.Context, *v1alpha1.DeviceStatus, ...CollectorOpt) error
}

type Getter interface {
	// Get returns the device status and is safe to call without a management client.
	Get(context.Context) *v1alpha1.DeviceStatus
}

type Manager interface {
	Getter
	// Sync collects status information from all exporters and updates the device status.
	Sync(context.Context) error
	// Collect gathers status information from all exporters and is safe to call without a management client.
	Collect(context.Context, ...CollectorOpt) error
	// RegisterStatusExporter registers an exporter to be called when collecting status.
	RegisterStatusExporter(Exporter)
	// Update updates the device status with the given update functions.
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error)
	// UpdateCondition updates the device status with the given condition.
	UpdateCondition(context.Context, v1alpha1.Condition) error
	// SetClient sets the management client for the status manager.
	SetClient(client.Management)
}

func (m *StatusManager) SetClient(managementClient client.Management) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.managementClient = managementClient
}

func (m *StatusManager) Get(ctx context.Context) *v1alpha1.DeviceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	// ensure status is immutable
	statusCopy := *m.device.Status
	return &statusCopy
}

// reset assumes the lock is held
func (m *StatusManager) reset() {
	m.device.Status.Applications = m.device.Status.Applications[:0]
}

func (m *StatusManager) RegisterStatusExporter(exporter Exporter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exporters = append(m.exporters, exporter)
}

func (m *StatusManager) Collect(ctx context.Context, opts ...CollectorOpt) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.collect(ctx, opts...)
}

// collect assumes the lock is held
func (m *StatusManager) collect(ctx context.Context, opts ...CollectorOpt) error {
	m.reset()

	errs := []error{}
	for _, export := range m.exporters {
		if err := export.Status(ctx, m.device.Status, opts...); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// ReloadCollect collects status information from all exporters in the case that
// the agent receives a SIGHUP signal.
func (m *StatusManager) ReloadCollect(ctx context.Context, _ *config.Config) error {
	// collect all status information from all exporters
	if err := m.Collect(ctx, WithForceCollect()); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}

	return nil
}

func (m *StatusManager) Sync(ctx context.Context) error {
	if m.managementClient == nil {
		m.log.Warn("management client not set")
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.collect(ctx); err != nil {
		return err
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

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.collect(ctx); err != nil {
		return err
	}

	changed := v1alpha1.SetStatusCondition(&m.device.Status.Conditions, condition)
	if !changed {
		return nil
	}

	err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device)
	if err != nil {
		return fmt.Errorf("failed to update device status: %w", err)
	}
	return nil
}

type UpdateStatusFn func(status *v1alpha1.DeviceStatus) error

func (m *StatusManager) Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1alpha1.DeviceStatus, error) {
	if m.managementClient == nil {
		return nil, fmt.Errorf("management client not set")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.collect(ctx); err != nil {
		return nil, err
	}

	for _, update := range updateFuncs {
		if err := update(m.device.Status); err != nil {
			return nil, err
		}
	}

	// TODO: handle retries
	if err := wait.PollUntilContextTimeout(ctx, 5*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device)
		if kerrors.IsNotFound(err) {
			return false, nil
		}

		if err != nil {
			return false, err
		}

		return true, nil
	}); err != nil {
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

func SetOSImage(osStatus v1alpha1.DeviceOsStatus) UpdateStatusFn {
	return func(status *v1alpha1.DeviceStatus) error {
		status.Os.Image = osStatus.Image
		status.Os.ImageDigest = osStatus.ImageDigest
		return nil
	}
}

type CollectorOpts struct {
	// Force forces the collection of status information from all exporters.
	Force bool
}

type CollectorOpt func(*CollectorOpts)

// WithForceCollect forces the collection of status information from all exporters.
func WithForceCollect() CollectorOpt {
	return func(o *CollectorOpts) {
		o.Force = true
	}
}
