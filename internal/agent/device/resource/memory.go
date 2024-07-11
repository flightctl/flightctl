package resource

import (
	"context"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	DefaultMemorySyncTimeout = 5 * time.Second
	DefaultProcMemInfoPath   = "/proc/meminfo"
)

var _ Monitor[MemoryUsage] = (*MemoryMonitor)(nil)

type MemoryMonitor struct {
	mu               sync.Mutex
	alerts           map[v1alpha1.ResourceAlertSeverityType]*Alert
	updateIntervalCh chan time.Duration
	usage            *MemoryUsage
	samplingInterval time.Duration
	memInfoPath      string

	log *log.PrefixLogger
}

func NewMemoryMonitor(
	log *log.PrefixLogger,
) *MemoryMonitor {
	return &MemoryMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		usage:            &MemoryUsage{},
		samplingInterval: DefaultSamplingInterval,
		memInfoPath:      DefaultProcMemInfoPath,
		log:              log,
	}
}

func (m *MemoryMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.getInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-m.updateIntervalCh:
			m.log.Infof("Updating memory monitor sampling interval to: %v", newInterval)
			ticker.Stop()
			ticker = time.NewTicker(newInterval)
		case <-ticker.C:
			m.log.Debug("Checking memory usage")
			usage := MemoryUsage{}
			m.sync(ctx, &usage)
			m.update(&usage)
			m.log.Debugf("Memory usage: %v", m.Usage())
		}
	}
}

// TODO: dedupe this with the other monitors

func (m *MemoryMonitor) Update(monitor v1alpha1.ResourceMonitor) (bool, error) {
	spec, err := monitor.AsMemoryResourceMonitorSpec()
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

func (m *MemoryMonitor) Alerts() []v1alpha1.ResourceAlertRule {
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

func (m *MemoryMonitor) Usage() *MemoryUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

func (m *MemoryMonitor) CollectUsage(ctx context.Context, usage *MemoryUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		file, err := os.ReadFile(m.memInfoPath)
		if err != nil {
			return err
		}

		lines := strings.Split(string(file), "\n")
		return parseMemStats(lines, usage)
	}
}

func (m *MemoryMonitor) sync(ctx context.Context, usage *MemoryUsage) {
	ctx, cancel := context.WithTimeout(ctx, DefaultMemorySyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		usage.err = err
	}

	m.update(usage)

	m.ensureAlerts()
}

func (m *MemoryMonitor) getInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
}

func (m *MemoryMonitor) setInterval(interval time.Duration) bool {
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

func (m *MemoryMonitor) deleteAlert(severity v1alpha1.ResourceAlertSeverityType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alerts, severity)
}

func (m *MemoryMonitor) setAlert(severity v1alpha1.ResourceAlertSeverityType, alert *Alert) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts[severity] = alert
}

func (m *MemoryMonitor) clearAlerts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = make(map[v1alpha1.ResourceAlertSeverityType]*Alert)
}

func parseMemStats(lines []string, usage *MemoryUsage) error {
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}

		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return err
		}

		switch key {
		case "MemTotal":
			usage.MemTotal = value
		case "MemFree":
			usage.MemFree = value
		case "Buffers":
			usage.Buffers = value
		case "Cached":
			usage.Cached = value
		case "SReclaimable":
			usage.SReclaimable = value
		case "Shmem":
			usage.Shmem = value
		}
	}

	// free memory is MemFree + Buffers + Cached + SReclaimable - Shmem
	freeMemory := usage.MemFree + usage.Buffers + usage.Cached + usage.SReclaimable - usage.Shmem
	usedMemory := usage.MemTotal - freeMemory

	if usage.MemTotal > 0 {
		usage.UsedPercent = int64((float64(usedMemory) / float64(usage.MemTotal)) * 100)
	} else {
		usage.UsedPercent = 0
	}

	usage.lastCollectedAt = time.Now()
	return nil
}

func (m *MemoryMonitor) ensureAlerts() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, alert := range m.alerts {
		if m.checkAlert(alert) {
			m.log.Debugf("Memory alert is firing: %s", alert.Severity)
		}
	}
}

func (m *MemoryMonitor) checkAlert(alert *Alert) bool {
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

func (m *MemoryMonitor) update(usage *MemoryUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
}

// MemoryUsage represents the memory usage of this device
type MemoryUsage struct {
	MemTotal     uint64
	MemFree      uint64
	Buffers      uint64
	Cached       uint64
	SReclaimable uint64
	Shmem        uint64

	UsedPercent int64

	lastCollectedAt time.Time
	err             error
}

func (u *MemoryUsage) Error() error {
	return u.err
}
