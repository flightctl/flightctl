package resource

import (
	"context"
	"math"
	"reflect"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	DefaultDiskSyncTimeout = 5 * time.Second
)

var _ Monitor[DiskUsage] = (*DiskMonitor)(nil)

type DiskMonitor struct {
	mu               sync.Mutex
	alerts           map[v1alpha1.ResourceAlertSeverityType]*Alert
	updateIntervalCh chan time.Duration
	path             string
	usage            *DiskUsage
	samplingInterval time.Duration

	log *log.PrefixLogger
}

func NewDiskMonitor(
	log *log.PrefixLogger,
) *DiskMonitor {
	return &DiskMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		usage:            &DiskUsage{},
		samplingInterval: DefaultSamplingInterval,
		log:              log,
	}
}

func (m *DiskMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.getInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-m.updateIntervalCh:
			m.log.Infof("Updating disk monitor sampling interval to: %v", newInterval)
			ticker.Stop()
			ticker = time.NewTicker(newInterval)
		case <-ticker.C:
			m.log.Debug("Checking disk usage")
			usage := DiskUsage{}
			m.sync(ctx, &usage)
			m.update(&usage)
			m.log.Debugf("Disk usage: %v", m.Usage())
		}
	}
}

// TODO: dedupe this with the other monitors

func (m *DiskMonitor) Update(monitor v1alpha1.ResourceMonitor) (bool, error) {
	spec, err := monitor.AsDiskResourceMonitorSpec()
	if err != nil {
		return false, err
	}

	updated := false
	if len(spec.AlertRules) == 0 {
		m.clearAlerts()
		updated = true
	}

	newSamplingInterval, err := time.ParseDuration(spec.SamplingInterval)
	if err != nil {
		return false, err
	}

	seen := make(map[v1alpha1.ResourceAlertSeverityType]struct{})
	for _, rule := range spec.AlertRules {
		seen[rule.Severity] = struct{}{}
	}

	// remove any alerts that are no longer in the spec
	for severity := range m.alerts {
		if _, ok := seen[severity]; !ok {
			m.deleteAlert(severity)
			updated = true
		}
	}

	for _, rule := range spec.AlertRules {
		alert, ok := m.alerts[rule.Severity]
		if !ok {
			alert, err = NewAlert(rule)
			if err != nil {
				return false, err
			}
			m.setAlert(rule.Severity, alert)
			updated = true
		}

		if spec.Path != m.path {
			m.setPath(spec.Path)
			updated = true
		}
		if !reflect.DeepEqual(alert.ResourceAlertRule, rule) {
			alert.ResourceAlertRule = rule
			updated = true
		}
	}

	if m.setInterval(newSamplingInterval) {
		updated = true
	}

	return updated, nil
}

func (m *DiskMonitor) Alerts() []v1alpha1.ResourceAlertRule {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firing []v1alpha1.ResourceAlertRule
	for _, alert := range m.alerts {
		if alert.IsFiring() {
			firing = append(firing, alert.ResourceAlertRule)
		}
	}
	return firing
}

func (m *DiskMonitor) Usage() *DiskUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

func (m *DiskMonitor) CollectUsage(ctx context.Context, usage *DiskUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		diskInfo, err := getDirUsage(m.path)
		if err != nil {
			return err
		}
		usage.Total += diskInfo.Total
		usage.Free += diskInfo.Free
		usage.Used += diskInfo.Used
		usage.UsedPercent = percentageDiskUsed(usage.Free, usage.Total)
		usage.lastCollectedAt = time.Now()
	}
	return nil
}

func (m *DiskMonitor) sync(ctx context.Context, usage *DiskUsage) {
	ctx, cancel := context.WithTimeout(ctx, DefaultDiskSyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		usage.err = err
	}

	m.update(usage)

	m.ensureAlerts()
}

func (m *DiskMonitor) ensureAlerts() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, alert := range m.alerts {
		if m.checkAlert(alert) {
			m.log.Debugf("Disk alert is firing: %s", alert.Severity)
		}
	}
}

func (m *DiskMonitor) checkAlert(alert *Alert) bool {
	// if the available usage is below the threshold, reset the firingSince time
	if m.usage.UsedPercent > int64(alert.Percentage) {
		if alert.firingSince.IsZero() {
			alert.firingSince = time.Now()
		}
	} else {
		// reset
		alert.firingSince = time.Time{}
	}

	// if the alert has been firing for the duration, set the firing flag
	if !alert.firingSince.IsZero() && time.Since(alert.firingSince) > alert.duration {
		alert.firing = true
	}

	return alert.IsFiring()
}

func (m *DiskMonitor) update(usage *DiskUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
}

func (m *DiskMonitor) getInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
}

func (m *DiskMonitor) setInterval(interval time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if interval != m.samplingInterval {
		m.samplingInterval = interval
		select {
		case m.updateIntervalCh <- m.samplingInterval:
		default:
			// don't block
		}
		return true
	}

	return false
}

func (m *DiskMonitor) setPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path

}

func (m *DiskMonitor) deleteAlert(severity v1alpha1.ResourceAlertSeverityType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alerts, severity)
}

func (m *DiskMonitor) setAlert(severity v1alpha1.ResourceAlertSeverityType, alert *Alert) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts[severity] = alert
}

func (m *DiskMonitor) clearAlerts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = make(map[v1alpha1.ResourceAlertSeverityType]*Alert)
}

func getDirUsage(dir string) (*DiskUsage, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(dir, &stat)
	if err != nil {
		return nil, err
	}

	return &DiskUsage{
		Inodes: stat.Files,
		Total:  stat.Blocks * uint64(stat.Bsize),
		Free:   stat.Bavail * uint64(stat.Bsize),
		Used:   (stat.Blocks - stat.Bfree) * uint64(stat.Bsize),
	}, nil
}

func percentageDiskUsed(free, total uint64) int64 {
	if total == 0 {
		return 0
	}
	percentage := (1 - (float64(free) / float64(total))) * 100
	return int64(math.Round(percentage))
}

// DiskUsage represents the tracked Disk usage of this device.
type DiskUsage struct {
	Inodes      uint64
	Total       uint64
	Free        uint64
	Used        uint64
	UsedPercent int64

	lastCollectedAt time.Time
	err             error
}

func (u *DiskUsage) Error() error {
	return u.err
}
