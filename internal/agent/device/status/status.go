package status

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/mohae/deepcopy"
)

const (
	MaxMessageLength    = 250
	statusUpdateTimeout = 60 * time.Second
)

var _ Manager = (*StatusManager)(nil)

// StatusContribution holds the status data returned by an exporter.
// Each exporter populates only the fields it owns; nil fields are ignored
// during merge.
//
// If an exporter returns both a non-nil StatusContribution and a non-nil
// error, the contribution is still merged (the exporter did its best) and
// the error is aggregated with other exporter errors.
type StatusContribution struct {
	Applications        []v1beta1.DeviceApplicationStatus
	ApplicationsSummary *v1beta1.DeviceApplicationsSummaryStatus
	Systemd             *[]v1beta1.SystemdUnitStatus
	Resources           *v1beta1.DeviceResourceStatus
	Os                  *v1beta1.DeviceOsStatus
	Config              *v1beta1.DeviceConfigStatus
	SystemInfo          *v1beta1.DeviceSystemInfo
	// SummaryContribution lets an exporter influence the device summary
	// without setting it directly. The manager picks highest severity.
	SummaryContribution *SummaryContribution
}

// SummaryContribution carries an exporter's desired summary severity.
type SummaryContribution struct {
	Status v1beta1.DeviceSummaryStatusType
	Info   *string
}

// summaryPriority defines the ordering for picking the highest-severity summary.
var summaryPriority = map[v1beta1.DeviceSummaryStatusType]int{
	v1beta1.DeviceSummaryStatusUnknown:   0,
	v1beta1.DeviceSummaryStatusOnline:    1,
	v1beta1.DeviceSummaryStatusDegraded:  2,
	v1beta1.DeviceSummaryStatusRebooting: 3,
	v1beta1.DeviceSummaryStatusError:     4,
}

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	log *log.PrefixLogger,
	exporters []Exporter,
	configWarnings []string,
) *StatusManager {
	status := v1beta1.NewDeviceStatus()
	return &StatusManager{
		deviceName: deviceName,
		device: &v1beta1.Device{
			ApiVersion: v1beta1.DeviceAPIVersion,
			Kind:       v1beta1.DeviceKind,
			Metadata: v1beta1.ObjectMeta{
				Name: &deviceName,
			},
			Status: &status,
		},
		exporters:      exporters,
		configWarnings: configWarnings,
		log:            log,
	}
}

// StatusManager aggregates device status from various exporters.
type StatusManager struct {
	mu               sync.Mutex
	deviceName       string
	managementClient client.Management
	exporters        []Exporter
	configWarnings   []string
	device           *v1beta1.Device
	lastStatus       *v1beta1.DeviceStatus

	log *log.PrefixLogger
}

// Exporter returns a StatusContribution describing the exporter's owned fields.
type Exporter interface {
	Status(context.Context, ...CollectorOpt) (*StatusContribution, error)
}

// Getter provides read access to the device status.
type Getter interface {
	// Get returns the device status and is safe to call without a management client.
	Get(context.Context) *v1beta1.DeviceStatus
}

// Manager is the consumer-facing interface for device status operations.
type Manager interface {
	Getter
	// Sync collects status information from all exporters and updates the device status.
	Sync(context.Context) error
	// Collect gathers status information from all exporters and is safe to call without a management client.
	Collect(context.Context, ...CollectorOpt) error
	// Update updates the device status with the given update functions.
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1beta1.DeviceStatus, error)
	// UpdateCondition updates the device status with the given condition.
	UpdateCondition(context.Context, v1beta1.Condition) error
	// SetClient sets the management client for the status manager.
	SetClient(client.Management)
	// InvalidateLastStatus clears the in-memory last pushed status so the next Sync will push again.
	InvalidateLastStatus()
}

func (m *StatusManager) SetClient(managementClient client.Management) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.managementClient = managementClient
}

func (m *StatusManager) InvalidateLastStatus() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastStatus = nil
}

func (m *StatusManager) Get(ctx context.Context) *v1beta1.DeviceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()

	// ensure status is immutable
	statusCopy := *m.device.Status
	return &statusCopy
}

// reset assumes the lock is held — only Applications is zeroed; other fields
// carry forward from the previous cycle.
func (m *StatusManager) reset() {
	m.device.Status.Applications = m.device.Status.Applications[:0]
}

func (m *StatusManager) Collect(ctx context.Context, opts ...CollectorOpt) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.collect(ctx, opts...)
}

// collect assumes the lock is held
func (m *StatusManager) collect(ctx context.Context, opts ...CollectorOpt) error {
	m.reset()

	var errs []error
	var summaryContributions []*SummaryContribution

	for _, export := range m.exporters {
		contribution, err := export.Status(ctx, opts...)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			errs = append(errs, err)
		}
		if contribution != nil {
			mergeContribution(m.device.Status, contribution)
			if contribution.SummaryContribution != nil {
				summaryContributions = append(summaryContributions, contribution.SummaryContribution)
			}
		}
	}

	m.device.Status.Summary = calculateSummary(summaryContributions, m.configWarnings)

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// mergeContribution applies non-nil fields from a StatusContribution onto
// the existing DeviceStatus. Nil fields are left untouched (carry-forward).
func mergeContribution(s *v1beta1.DeviceStatus, c *StatusContribution) {
	if c == nil {
		return
	}
	if c.Applications != nil {
		s.Applications = append(s.Applications, c.Applications...)
	}
	if c.ApplicationsSummary != nil {
		s.ApplicationsSummary = *c.ApplicationsSummary
	}
	if c.Systemd != nil {
		if *c.Systemd == nil {
			s.Systemd = nil
		} else {
			s.Systemd = c.Systemd
		}
	}
	if c.Resources != nil {
		s.Resources = *c.Resources
	}
	if c.Os != nil {
		s.Os = *c.Os
	}
	if c.Config != nil {
		s.Config = *c.Config
	}
	if c.SystemInfo != nil {
		s.SystemInfo = *c.SystemInfo
	}
}

// calculateSummary picks the highest-severity SummaryContribution using
// summaryPriority ordering. If configWarnings are present and no higher
// severity exists, returns Degraded with the joined warning message.
// If no contributions exist, returns Online with nil Info.
func calculateSummary(contributions []*SummaryContribution, configWarnings []string) v1beta1.DeviceSummaryStatus {
	var best *SummaryContribution
	bestPriority := -1

	for _, c := range contributions {
		if c == nil {
			continue
		}
		p := summaryPriority[c.Status]
		if p >= bestPriority {
			bestPriority = p
			best = c
		}
	}

	// Config warnings contribute Degraded if no higher severity exists
	if len(configWarnings) > 0 {
		degradedPriority := summaryPriority[v1beta1.DeviceSummaryStatusDegraded]
		if bestPriority < degradedPriority {
			msg := log.Truncate(strings.Join(configWarnings, "; "), MaxMessageLength)
			return v1beta1.DeviceSummaryStatus{
				Status: v1beta1.DeviceSummaryStatusDegraded,
				Info:   &msg,
			}
		}
	}

	if best == nil {
		return v1beta1.DeviceSummaryStatus{
			Status: v1beta1.DeviceSummaryStatusOnline,
		}
	}

	return v1beta1.DeviceSummaryStatus{
		Status: best.Status,
		Info:   best.Info,
	}
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

// update pushes the current device status to the management server.
// Returns true if the status was sent or no changes require an update.
// Returns false on failure; lastStatus is left unchanged to allow retry.
func (m *StatusManager) update(ctx context.Context) bool {
	if reflect.DeepEqual(m.lastStatus, m.device.Status) {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, statusUpdateTimeout)
	defer cancel()
	if err := m.managementClient.UpdateDeviceStatus(ctx, m.deviceName, *m.device); err != nil {
		m.log.Warnf("Failed to update device status: %v", err)
		return false
	}
	st, ok := deepcopy.Copy(m.device.Status).(*v1beta1.DeviceStatus)
	if !ok {
		m.log.Warn("Failed to deep copy device status")
		return false
	}
	m.lastStatus = st
	return true
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

	if !m.update(ctx) {
		return deviceerrors.ErrFailedToPushStatus
	}
	return nil
}

func (m *StatusManager) UpdateCondition(ctx context.Context, condition v1beta1.Condition) error {
	if m.managementClient == nil {
		return fmt.Errorf("management client not set")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.collect(ctx); err != nil {
		return err
	}

	changed := v1beta1.SetStatusCondition(&m.device.Status.Conditions, condition)
	if !changed {
		return nil
	}

	if !m.update(ctx) {
		return deviceerrors.ErrFailedToPushStatus
	}

	return nil
}

type UpdateStatusFn func(status *v1beta1.DeviceStatus) error

func (m *StatusManager) Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1beta1.DeviceStatus, error) {
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

	if !m.update(ctx) {
		return nil, deviceerrors.ErrFailedToPushStatus
	}

	return m.device.Status, nil
}

func SetDeviceSummary(summaryStatus v1beta1.DeviceSummaryStatus) UpdateStatusFn {
	return func(status *v1beta1.DeviceStatus) error {
		status.Summary.Status = summaryStatus.Status
		status.Summary.Info = summaryStatus.Info
		return nil
	}
}

func SetConfig(configStatus v1beta1.DeviceConfigStatus) UpdateStatusFn {
	return func(status *v1beta1.DeviceStatus) error {
		status.Config.RenderedVersion = configStatus.RenderedVersion
		return nil
	}
}

func SetCondition(condition v1beta1.Condition) UpdateStatusFn {
	return func(status *v1beta1.DeviceStatus) error {
		v1beta1.SetStatusCondition(&status.Conditions, condition)
		return nil
	}
}

func SetOSImage(osStatus v1beta1.DeviceOsStatus) UpdateStatusFn {
	return func(status *v1beta1.DeviceStatus) error {
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
