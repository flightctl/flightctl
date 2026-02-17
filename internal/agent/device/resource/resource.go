package resource

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type MonitorType string

const (
	CPUMonitorType    = "CPU"
	DiskMonitorType   = "Disk"
	MemoryMonitorType = "Memory"

	DefaultSamplingInterval = 1 * time.Minute
)

type Manager interface {
	Run(ctx context.Context)
	Update(monitor *v1beta1.ResourceMonitor) (bool, error)
	// ResetAlertDefaults clears all alerts and resets the monitors to their default state.
	ResetAlertDefaults() error
	Alerts() *Alerts
	BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error
	IsCriticalAlert(monitorType MonitorType) bool
	GetFiringCriticalAlerts(monitorType MonitorType) []v1beta1.ResourceAlertRule
	status.Exporter
}

type Monitor[T any] interface {
	Run(ctx context.Context)
	Update(monitor *v1beta1.ResourceMonitor) (bool, error)
	Alerts() []v1beta1.ResourceAlertRule
}

type Collector[T any] interface {
	CollectUsage(ctx context.Context, usage *T) error
}

type ResourceManager struct {
	cpuMonitor    Monitor[CPUUsage]
	diskMonitor   Monitor[DiskUsage]
	memoryMonitor Monitor[MemoryUsage]
	log           *log.PrefixLogger
}

// NewManager creates a new resource Manager.
func NewManager(
	log *log.PrefixLogger,
) Manager {
	return &ResourceManager{
		cpuMonitor:    NewCPUMonitor(log),
		diskMonitor:   NewDiskMonitor(log),
		memoryMonitor: NewMemoryMonitor(log),
		log:           log,
	}
}

func (m *ResourceManager) Run(ctx context.Context) {
	m.log.Debug("Starting resource manager")
	defer m.log.Debug("Resource manager stopped")

	go m.diskMonitor.Run(ctx)
	go m.cpuMonitor.Run(ctx)
	go m.memoryMonitor.Run(ctx)

	<-ctx.Done()
}

func (m *ResourceManager) Update(monitor *v1beta1.ResourceMonitor) (bool, error) {
	monitorType, err := monitor.Discriminator()
	if err != nil {
		return false, err
	}

	switch monitorType {
	case CPUMonitorType:
		return m.cpuMonitor.Update(monitor)
	case DiskMonitorType:
		return m.diskMonitor.Update(monitor)
	case MemoryMonitorType:
		return m.memoryMonitor.Update(monitor)
	default:
		return false, fmt.Errorf("unknown monitor type: %s", monitorType)
	}
}

func (m *ResourceManager) ResetAlertDefaults() error {
	var errs []error

	// cpu
	cpuMonitor, err := defaultCPUResourceMonitor()
	if err != nil {
		errs = append(errs, err)
	}
	updated, err := m.cpuMonitor.Update(cpuMonitor)
	if err != nil {
		errs = append(errs, err)
	}
	if updated {
		m.log.Debug("Reset CPU monitor alerts")
	}

	// disk
	diskMonitor, err := defaultDiskResourceMonitor()
	if err != nil {
		errs = append(errs, err)
	}
	updated, err = m.diskMonitor.Update(diskMonitor)
	if err != nil {
		errs = append(errs, err)
	}
	if updated {
		m.log.Debug("Reset disk monitor alerts")
	}

	// memory
	memoryMonitor, err := defaultMemoryResourceMonitor()
	if err != nil {
		errs = append(errs, err)
	}
	updated, err = m.memoryMonitor.Update(memoryMonitor)
	if err != nil {
		errs = append(errs, err)
	}
	if updated {
		m.log.Debug("Reset memory monitor alerts")
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// Status returns the device status based on the resource monitors and for the device summary.
func (m *ResourceManager) Status(ctx context.Context, status *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	alerts := m.Alerts()

	hasCriticalOrErrorResource := false
	hasDegradedResource := false
	criticalResourceTypes := []string{}
	degradedResourceTypes := []string{}

	// define monitor types and alerts
	resourceMonitors := map[string]struct {
		alerts      []v1beta1.ResourceAlertRule
		setStatusFn func(v1beta1.DeviceResourceStatusType)
	}{
		DiskMonitorType: {
			alerts: alerts.DiskUsage,
			setStatusFn: func(resourceStatus v1beta1.DeviceResourceStatusType) {
				status.Resources.Disk = resourceStatus
			},
		},
		CPUMonitorType: {
			alerts: alerts.CPUUsage,
			setStatusFn: func(resourceStatus v1beta1.DeviceResourceStatusType) {
				status.Resources.Cpu = resourceStatus
			},
		},
		MemoryMonitorType: {
			alerts: alerts.MemoryUsage,
			setStatusFn: func(resourceStatus v1beta1.DeviceResourceStatusType) {
				status.Resources.Memory = resourceStatus
			},
		},
	}

	// set the status for each monitor type
	for monitorType, monitor := range resourceMonitors {
		resourceStatus, alertMsg := getHighestSeverityResourceStatusFromAlerts(monitorType, monitor.alerts)
		if alertMsg != "" {
			m.log.Warn(alertMsg)
		}

		switch resourceStatus {
		case v1beta1.DeviceResourceStatusCritical, v1beta1.DeviceResourceStatusError:
			hasCriticalOrErrorResource = true
			criticalResourceTypes = append(criticalResourceTypes, monitorType)
		case v1beta1.DeviceResourceStatusWarning:
			hasDegradedResource = true
			degradedResourceTypes = append(degradedResourceTypes, monitorType)
		}

		monitor.setStatusFn(resourceStatus)
	}

	// ensure status proper reflects in the device summary
	if hasCriticalOrErrorResource {
		status.Summary.Status = v1beta1.DeviceSummaryStatusError
		status.Summary.Info = lo.ToPtr(fmt.Sprintf("Critical resource alert: %s", strings.Join(criticalResourceTypes, ", ")))
	} else if hasDegradedResource {
		status.Summary.Status = v1beta1.DeviceSummaryStatusDegraded
		status.Summary.Info = lo.ToPtr(fmt.Sprintf("Degraded resource alert: %s", strings.Join(degradedResourceTypes, ", ")))
	} else {
		status.Summary.Status = v1beta1.DeviceSummaryStatusOnline
		status.Summary.Info = nil
	}

	return nil
}

func (m *ResourceManager) Alerts() *Alerts {
	return &Alerts{
		DiskUsage:   m.diskMonitor.Alerts(),
		CPUUsage:    m.cpuMonitor.Alerts(),
		MemoryUsage: m.memoryMonitor.Alerts(),
	}
}

// isCriticalAlert checks if there are any critical level alerts for the specified resource type.
func (m *ResourceManager) isCriticalAlert(monitorType MonitorType) bool {
	alerts := m.Alerts()

	var alertList []v1beta1.ResourceAlertRule

	switch monitorType {
	case DiskMonitorType:
		alertList = alerts.DiskUsage
	case CPUMonitorType:
		alertList = alerts.CPUUsage
	case MemoryMonitorType:
		alertList = alerts.MemoryUsage
	default:
		m.log.Warnf("Unknown monitor type: %s", monitorType)
		return false
	}

	for _, alert := range alertList {
		if alert.Severity == v1beta1.ResourceAlertSeverityTypeCritical {
			return true
		}
	}

	return false
}

// GetFiringCriticalAlerts returns all firing critical alerts for a given monitor type.
func (m *ResourceManager) GetFiringCriticalAlerts(monitorType MonitorType) []v1beta1.ResourceAlertRule {
	alerts := m.Alerts()

	var alertList []v1beta1.ResourceAlertRule

	switch monitorType {
	case DiskMonitorType:
		alertList = alerts.DiskUsage
	case CPUMonitorType:
		alertList = alerts.CPUUsage
	case MemoryMonitorType:
		alertList = alerts.MemoryUsage
	default:
		m.log.Warnf("Unknown monitor type: %s", monitorType)
		return nil
	}

	criticalAlerts := []v1beta1.ResourceAlertRule{}
	for _, alert := range alertList {
		if alert.Severity == v1beta1.ResourceAlertSeverityTypeCritical {
			criticalAlerts = append(criticalAlerts, alert)
		}
	}

	return criticalAlerts
}

// Sync applies resource monitor configuration from the desired spec.
func (m *ResourceManager) sync(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if desired.Resources == nil {
		m.log.Debug("Device resources are nil, resetting to defaults")
		return m.ResetAlertDefaults()
	}

	for i := range *desired.Resources {
		monitor := (*desired.Resources)[i]
		if _, err := m.Update(&monitor); err != nil {
			return err
		}
	}

	return nil
}

// BeforeUpdate syncs resource monitors with the desired spec.
func (m *ResourceManager) BeforeUpdate(ctx context.Context, desired *v1beta1.DeviceSpec) error {
	return m.sync(ctx, desired)
}

// IsCriticalAlert checks if there is a critical level alert for the specified resource type.
func (m *ResourceManager) IsCriticalAlert(monitorType MonitorType) bool {
	return m.isCriticalAlert(monitorType)
}

type Alerts struct {
	DiskUsage   []v1beta1.ResourceAlertRule
	CPUUsage    []v1beta1.ResourceAlertRule
	MemoryUsage []v1beta1.ResourceAlertRule
}

type Alert struct {
	mu sync.Mutex
	v1beta1.ResourceAlertRule

	// duration is the time the alert must be observed before it is considered firing
	// this is a helper field to avoid parsing the duration string on every sync.
	// it is set when the alert is using NewAlert or must be set manually.
	duration time.Duration
	// firingSince is the time the alert started firing
	firingSince time.Time
	firing      bool
}

func NewAlert(rule v1beta1.ResourceAlertRule) (*Alert, error) {
	duration, err := time.ParseDuration(rule.Duration)
	if err != nil {
		return nil, err
	}
	return &Alert{
		ResourceAlertRule: rule,
		duration:          duration,
	}, nil
}

func (a *Alert) Sync(usagePercentage int64) {
	a.mu.Lock()
	defer a.mu.Unlock()

	// if the available usage is below the threshold, reset the firingSince time
	if usagePercentage > int64(a.Percentage) {
		if a.firingSince.IsZero() {
			a.firingSince = time.Now()
		}
	} else {
		// reset
		a.firingSince = time.Time{}
	}

	// if the alert has been firing for the duration, set the firing flag
	if isDurationExceeded(a.firingSince, a.duration) {
		a.firing = true
	} else {
		a.firing = false
	}
}

func isDurationExceeded(since time.Time, duration time.Duration) bool {
	return !since.IsZero() && time.Since(since) > duration
}

func (a *Alert) IsFiring() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.firing
}

// UpdateRule updates the alert rule and duration.
func (a *Alert) UpdateRule(rule v1beta1.ResourceAlertRule) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	duration, err := time.ParseDuration(rule.Duration)
	if err != nil {
		return err
	}
	a.ResourceAlertRule = rule
	a.duration = duration
	return nil
}

var AlertLevelMap = map[v1beta1.ResourceAlertSeverityType]AlertSeverity{
	v1beta1.ResourceAlertSeverityTypeInfo:     {Status: v1beta1.DeviceResourceStatusHealthy, Level: 0},
	v1beta1.ResourceAlertSeverityTypeWarning:  {Status: v1beta1.DeviceResourceStatusWarning, Level: 1},
	v1beta1.ResourceAlertSeverityTypeCritical: {Status: v1beta1.DeviceResourceStatusCritical, Level: 2},
}

type AlertSeverity struct {
	Status v1beta1.DeviceResourceStatusType
	Level  int
}

// MonitorSpec is a wrapper around v1beta1.ResourceMonitorSpec that includes additional fields for all monitors.
type MonitorSpec struct {
	v1beta1.ResourceMonitorSpec

	// Path is the absolute path used for the disk monitor.
	Path string `json:"path,omitempty"`
}

// getHighestSeverityResourceStatusFromAlerts returns the highest severity statusDeviceResourceStatusType from a list of alerts along with the alert message.
// The alert message is auto generated if the alert description is empty.
func getHighestSeverityResourceStatusFromAlerts(resource string, alerts []v1beta1.ResourceAlertRule) (v1beta1.DeviceResourceStatusType, string) {
	if len(alerts) == 0 {
		return v1beta1.DeviceResourceStatusHealthy, ""
	}

	var info string
	// initialize with the lowest severity (info)
	maxSeverity := AlertLevelMap[v1beta1.ResourceAlertSeverityTypeInfo]
	var highestSeverity v1beta1.DeviceResourceStatusType
	for _, alert := range alerts {
		severity := AlertLevelMap[alert.Severity]
		if severity.Level > maxSeverity.Level {
			maxSeverity = AlertLevelMap[alert.Severity]
			highestSeverity = severity.Status
			if alert.Description == "" {
				info = fmt.Sprintf("%s: %s usage is above %d %% for more than %s", alert.Severity, resource, int64(alert.Percentage), alert.Duration)
			} else {
				info = fmt.Sprintf("%s: %s", alert.Severity, alert.Description)
			}
		}
	}

	return highestSeverity, info
}

func FormatAlerts(alerts []v1beta1.ResourceAlertRule, monitorType MonitorType) string {
	var msgs []string
	for _, alert := range alerts {
		msg := fmt.Sprintf("%s usage is above %d%% for more than %s", monitorType, alert.Percentage, alert.Duration)
		if alert.Description != "" {
			msg = fmt.Sprintf("%s: %s", monitorType, alert.Description)
		}
		msgs = append(msgs, msg)
	}
	return strings.Join(msgs, ", ")
}

func defaultCPUResourceMonitor() (*v1beta1.ResourceMonitor, error) {
	spec := v1beta1.CpuResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      CPUMonitorType,
		AlertRules: []v1beta1.ResourceAlertRule{
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "30m",
				Description: "", // use generated description
			},
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "1h",
				Description: "", // use generated description
			},
		},
	}
	rm := &v1beta1.ResourceMonitor{}
	err := rm.FromCpuResourceMonitorSpec(spec)
	return rm, err
}

func defaultDiskResourceMonitor() (*v1beta1.ResourceMonitor, error) {
	spec := v1beta1.DiskResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      DiskMonitorType,
		AlertRules: []v1beta1.ResourceAlertRule{
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "10m",
				Description: "", // use generated description
			},
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "30m",
				Description: "", // use generated description
			},
		},
		Path: "/sysroot",
	}
	rm := &v1beta1.ResourceMonitor{}
	err := rm.FromDiskResourceMonitorSpec(spec)
	return rm, err
}

func defaultMemoryResourceMonitor() (*v1beta1.ResourceMonitor, error) {
	spec := v1beta1.MemoryResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      MemoryMonitorType,
		AlertRules: []v1beta1.ResourceAlertRule{
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "30m",
				Description: "", // use generated description
			},
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "1h",
				Description: "", // use generated description
			},
		},
	}
	rm := &v1beta1.ResourceMonitor{}
	err := rm.FromMemoryResourceMonitorSpec(spec)
	return rm, err
}
