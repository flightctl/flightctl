package resource

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
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
	Update(monitor *v1alpha1.ResourceMonitor) (bool, error)
	// ResetAlertDefaults clears all alerts and resets the monitors to their default state.
	ResetAlertDefaults() error
	Alerts() *Alerts
}

type Monitor[T any] interface {
	Run(ctx context.Context)
	Update(monitor *v1alpha1.ResourceMonitor) (bool, error)
	CollectUsage(ctx context.Context, usage *T) error
	Alerts() []v1alpha1.ResourceAlertRule
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
	m.log.Infof("Starting resource manager...")
	defer m.log.Infof("Resource manager stopped")

	go m.diskMonitor.Run(ctx)
	go m.cpuMonitor.Run(ctx)
	go m.memoryMonitor.Run(ctx)

	<-ctx.Done()
}

func (m *ResourceManager) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
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
		m.log.Infof("Reset CPU monitor alerts")
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
		m.log.Infof("Reset disk monitor alerts")
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
		m.log.Infof("Reset memory monitor alerts")
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
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

type Alerts struct {
	DiskUsage   []v1alpha1.ResourceAlertRule
	CPUUsage    []v1alpha1.ResourceAlertRule
	MemoryUsage []v1alpha1.ResourceAlertRule
}

type Alert struct {
	mu sync.Mutex
	v1alpha1.ResourceAlertRule

	// duration is the time the alert must be observed before it is considered firing
	// this is a helper field to avoid parsing the duration string on every sync.
	// it is set when the alert is using NewAlert or must be set manually.
	duration time.Duration
	// firingSince is the time the alert started firing
	firingSince time.Time
	firing      bool
}

func NewAlert(rule v1alpha1.ResourceAlertRule) (*Alert, error) {
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
func (a *Alert) UpdateRule(rule v1alpha1.ResourceAlertRule) error {
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

var AlertLevelMap = map[v1alpha1.ResourceAlertSeverityType]AlertSeverity{
	v1alpha1.ResourceAlertSeverityTypeInfo:     {Status: v1alpha1.DeviceResourceStatusHealthy, Level: 0},
	v1alpha1.ResourceAlertSeverityTypeWarning:  {Status: v1alpha1.DeviceResourceStatusWarning, Level: 1},
	v1alpha1.ResourceAlertSeverityTypeCritical: {Status: v1alpha1.DeviceResourceStatusCritical, Level: 2},
}

type AlertSeverity struct {
	Status v1alpha1.DeviceResourceStatusType
	Level  int
}

// MonitorSpec is a wrapper around v1alpha1.ResourceMonitorSpec that includes additional fields for all monitors.
type MonitorSpec struct {
	v1alpha1.ResourceMonitorSpec

	// Path is the absolute path used for the disk monitor.
	Path string `json:"path,omitempty"`
}

// GetHighestSeverityResourceStatusFromAlerts returns the highest severity statusDeviceResourceStatusType from a list of alerts along with the alert message.
// The alert message is auto generated if the alert description is empty.
func GetHighestSeverityResourceStatusFromAlerts(resource string, alerts []v1alpha1.ResourceAlertRule) (v1alpha1.DeviceResourceStatusType, string) {
	if len(alerts) == 0 {
		return v1alpha1.DeviceResourceStatusHealthy, ""
	}

	var info string
	// initialize with the lowest severity (info)
	maxSeverity := AlertLevelMap[v1alpha1.ResourceAlertSeverityTypeInfo]
	var highestSeverity v1alpha1.DeviceResourceStatusType
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

func defaultCPUResourceMonitor() (*v1alpha1.ResourceMonitor, error) {
	spec := v1alpha1.CPUResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      CPUMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "30m",
				Description: "", // use generated description
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "1h",
				Description: "", // use generated description
			},
		},
	}
	rm := &v1alpha1.ResourceMonitor{}
	err := rm.FromCPUResourceMonitorSpec(spec)
	return rm, err
}

func defaultDiskResourceMonitor() (*v1alpha1.ResourceMonitor, error) {
	spec := v1alpha1.DiskResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      DiskMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "10m",
				Description: "", // use generated description
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "30m",
				Description: "", // use generated description
			},
		},
		Path: "/sysroot",
	}
	rm := &v1alpha1.ResourceMonitor{}
	err := rm.FromDiskResourceMonitorSpec(spec)
	return rm, err
}

func defaultMemoryResourceMonitor() (*v1alpha1.ResourceMonitor, error) {
	spec := v1alpha1.MemoryResourceMonitorSpec{
		SamplingInterval: DefaultSamplingInterval.String(),
		MonitorType:      MemoryMonitorType,
		AlertRules: []v1alpha1.ResourceAlertRule{
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "30m",
				Description: "", // use generated description
			},
			{
				Severity:    v1alpha1.ResourceAlertSeverityTypeWarning,
				Percentage:  80,
				Duration:    "1h",
				Description: "", // use generated description
			},
		},
	}
	rm := &v1alpha1.ResourceMonitor{}
	err := rm.FromMemoryResourceMonitorSpec(spec)
	return rm, err
}
