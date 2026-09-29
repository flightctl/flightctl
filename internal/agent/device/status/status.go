package status

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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

// NewManager creates a new device status manager.
func NewManager(
	deviceName string,
	log *log.PrefixLogger,
	opts ...ManagerOption,
) *StatusManager {
	status := v1beta1.NewDeviceStatus()
	mgr := &StatusManager{
		deviceName: deviceName,
		device: &v1beta1.Device{
			ApiVersion: v1beta1.DeviceAPIVersion,
			Kind:       v1beta1.DeviceKind,
			Metadata: v1beta1.ObjectMeta{
				Name: &deviceName,
			},
			Status: &status,
		},
		log: log,
	}
	for _, opt := range opts {
		opt(mgr)
	}
	return mgr
}

// CriticalChangeNotifier returns a callback that components should call when
// their status changes in a way that warrants an immediate PATCH push. If no
// critical channel was provided via WithCriticalCh, returns a no-op.
func (m *StatusManager) CriticalChangeNotifier() func() {
	if m.criticalCh == nil {
		return func() {}
	}
	return func() {
		select {
		case m.criticalCh <- struct{}{}:
		default:
		}
	}
}

// StatusManager aggregates device status from various exporters.
type StatusManager struct {
	mu                sync.Mutex
	deviceName        string
	managementClient  client.Management
	exporters         []Exporter
	criticalExporters []Exporter
	criticalCh        chan struct{}
	device            *v1beta1.Device
	lastStatus        *v1beta1.DeviceStatus

	log *log.PrefixLogger
}

// ManagerOption configures optional StatusManager behavior.
type ManagerOption func(*StatusManager)

// WithCriticalCh injects the channel used to signal critical status changes.
// The StatusManager wraps its write-end in CriticalChangeNotifier().
func WithCriticalCh(ch chan struct{}) ManagerOption {
	return func(m *StatusManager) {
		m.criticalCh = ch
	}
}

type Exporter interface {
	// Status collects status information and updates the device status.
	Status(context.Context, *v1beta1.DeviceStatus, ...CollectorOpt) error
}

type Getter interface {
	// Get returns the device status and is safe to call without a management client.
	Get(context.Context) *v1beta1.DeviceStatus
}

type Manager interface {
	Getter
	// Sync collects status information from all exporters and updates the device status.
	Sync(context.Context) error
	// Collect gathers status information from all exporters and is safe to call without a management client.
	Collect(context.Context, ...CollectorOpt) error
	// RegisterStatusExporter registers an exporter to be called when collecting status.
	RegisterStatusExporter(Exporter)
	// RegisterCriticalExporter registers an exporter whose output is used
	// for diff-based PATCH updates when a status change is signaled.
	// Critical exporters use the same Exporter interface as collectors;
	// the diff determines which fields changed.
	RegisterCriticalExporter(Exporter)
	// Update updates the device status with the given update functions.
	Update(ctx context.Context, updateFuncs ...UpdateStatusFn) (*v1beta1.DeviceStatus, error)
	// UpdateCondition updates the device status with the given condition.
	UpdateCondition(context.Context, v1beta1.Condition) error
	// UpdateCritical runs registered patchers on a snapshot of the current
	// status, diffs the result to produce JSON Patch ops (RFC 6902), and
	// sends them via PatchDeviceStatus. Returns nil if no fields changed.
	UpdateCritical(ctx context.Context) error
	// CriticalChangeNotifier returns a callback for components to call when
	// their status changes in a way that warrants an immediate PATCH push.
	CriticalChangeNotifier() func()
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

// reset assumes the lock is held
func (m *StatusManager) reset() {
	m.device.Status.Applications = m.device.Status.Applications[:0]
}

func (m *StatusManager) RegisterStatusExporter(exporter Exporter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.exporters = append(m.exporters, exporter)
}

func (m *StatusManager) RegisterCriticalExporter(exporter Exporter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.criticalExporters = append(m.criticalExporters, exporter)
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

// UpdateCritical runs registered patchers on a deep copy of the current status,
// diffs the before/after to generate RFC 6902 JSON Patch replace ops, and
// sends them via PatchDeviceStatus. Returns nil if no fields changed.
func (m *StatusManager) UpdateCritical(ctx context.Context) error {
	m.mu.Lock()
	if len(m.criticalExporters) == 0 {
		m.mu.Unlock()
		return nil
	}

	if m.managementClient == nil {
		m.mu.Unlock()
		return fmt.Errorf("management client not set")
	}

	before, ok := deepcopy.Copy(m.device.Status).(*v1beta1.DeviceStatus)
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("failed to deep copy device status for patch")
	}
	after, ok := deepcopy.Copy(m.device.Status).(*v1beta1.DeviceStatus)
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("failed to deep copy device status for patch")
	}

	for _, p := range m.criticalExporters {
		if err := p.Status(ctx, after); err != nil {
			m.mu.Unlock()
			return fmt.Errorf("critical exporter failed: %w", err)
		}
	}
	m.mu.Unlock()

	ops, err := diffStatusToPatch("/status", before, after)
	if err != nil {
		return fmt.Errorf("generating patch ops: %w", err)
	}
	if len(ops) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, statusUpdateTimeout)
	defer cancel()
	if err := m.managementClient.PatchDeviceStatus(ctx, m.deviceName, ops); err != nil {
		return err
	}

	// Update lastStatus so the next periodic Sync sees the patched state
	// and skips a redundant PUT for fields that were already pushed.
	m.mu.Lock()
	m.lastStatus = after
	m.mu.Unlock()
	return nil
}

// diffStatusToPatch marshals before and after to flat JSON maps and produces
// RFC 6902 replace ops for every top-level field that changed. Uses only
// encoding/json from stdlib — no third-party dependencies.
func diffStatusToPatch(prefix string, before, after *v1beta1.DeviceStatus) (v1beta1.PatchRequest, error) {
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return nil, fmt.Errorf("marshal before: %w", err)
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return nil, fmt.Errorf("marshal after: %w", err)
	}

	var beforeMap map[string]json.RawMessage
	var afterMap map[string]json.RawMessage
	if err := json.Unmarshal(beforeJSON, &beforeMap); err != nil {
		return nil, fmt.Errorf("unmarshal before: %w", err)
	}
	if err := json.Unmarshal(afterJSON, &afterMap); err != nil {
		return nil, fmt.Errorf("unmarshal after: %w", err)
	}

	var ops v1beta1.PatchRequest
	for key, afterVal := range afterMap {
		beforeVal, exists := beforeMap[key]
		if !exists || string(beforeVal) != string(afterVal) {
			var val interface{}
			if err := json.Unmarshal(afterVal, &val); err != nil {
				return nil, fmt.Errorf("unmarshal field %s: %w", key, err)
			}
			ops = append(ops, struct {
				Op    v1beta1.PatchRequestOp `json:"op"`
				Path  string                 `json:"path"`
				Value interface{}            `json:"value,omitempty"`
			}{
				Op:    v1beta1.Replace,
				Path:  prefix + "/" + key,
				Value: val,
			})
		}
	}
	return ops, nil
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
