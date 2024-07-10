package resource

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	CPUMonitorType    = "CPU"
	DiskMonitorType   = "Disk"
	MemoryMonitorType = "Memory"

	DefaultSamplingInterval = 1 * time.Hour
)

type Manager interface {
	Run(ctx context.Context)
	Update(monitor v1alpha1.ResourceMonitor) (bool, error)
	Alerts() *Alerts
}

type Monitor[T any] interface {
	Run(ctx context.Context)
	Update(monitor v1alpha1.ResourceMonitor) (bool, error)
	Usage() *T
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
	m.memoryMonitor.Run(ctx)
}

func (m *ResourceManager) Update(monitor v1alpha1.ResourceMonitor) (bool, error) {
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

func (m *ResourceManager) Alerts() *Alerts {
	return &Alerts{
		DiskUsage: m.diskMonitor.Alerts(),
		CPUUsage:  m.cpuMonitor.Alerts(),
	}
}

type Alerts struct {
	DiskUsage   []v1alpha1.ResourceAlertRule
	CPUUsage    []v1alpha1.ResourceAlertRule
	MemoryUsage []v1alpha1.ResourceAlertRule
}

type Alert struct {
	v1alpha1.ResourceAlertRule
	duration    time.Duration
	firing      bool
	firingSince time.Time
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

func (a *Alert) IsFiring() bool {
	return a.firing
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

// GetHighestSeverityResourceStatusFromAlerts returns the highest severity statusDeviceResourceStatusType from a list of alerts.
func GetHighestSeverityResourceStatusFromAlerts(alerts []v1alpha1.ResourceAlertRule) (v1alpha1.DeviceResourceStatusType, error) {
	if len(alerts) == 0 {
		return v1alpha1.DeviceResourceStatusUnknown, nil
	}

	// initialize with the lowest severity (info)
	maxSeverity := AlertLevelMap[v1alpha1.ResourceAlertSeverityTypeInfo]
	var highestSeverity v1alpha1.DeviceResourceStatusType
	for _, alert := range alerts {
		severity := AlertLevelMap[alert.Severity]
		if severity.Level > maxSeverity.Level {
			maxSeverity = AlertLevelMap[alert.Severity]
			highestSeverity = severity.Status
		}
	}

	return highestSeverity, nil
}
