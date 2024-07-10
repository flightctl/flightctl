package resource

import (
	"bufio"
	"context"
	"fmt"
	"math"
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
	DefaultCPUSyncTimeout = 5 * time.Second
	DefaultProcStatPath   = "/proc/stat"
)

var _ Monitor[CPUUsage] = (*CPUMonitor)(nil)

type CPUMonitor struct {
	mu               sync.Mutex
	alerts           map[v1alpha1.ResourceAlertSeverityType]*Alert
	updateIntervalCh chan time.Duration
	samplingInterval time.Duration
	usage            *CPUUsage
	statPath         string

	log *log.PrefixLogger
}

func NewCPUMonitor(
	log *log.PrefixLogger,
) *CPUMonitor {
	return &CPUMonitor{
		usage:            &CPUUsage{},
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		statPath:         DefaultProcStatPath,
		samplingInterval: DefaultSamplingInterval,
		log:              log,
	}
}

func (m *CPUMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.getInterval())
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-m.updateIntervalCh:
			m.log.Infof("Updating cpu monitor sampling interval to: %s", newInterval)
			ticker.Stop()
			ticker = time.NewTicker(newInterval)
		case <-ticker.C:
			m.log.Debug("Checking disk usage")
			usage := &CPUUsage{}
			m.sync(ctx, usage)
			m.update(usage)
		}
	}
}

func (m *CPUMonitor) Update(monitor v1alpha1.ResourceMonitor) (bool, error) {
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

func (m *CPUMonitor) Alerts() []v1alpha1.ResourceAlertRule {
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

func (m *CPUMonitor) Usage() *CPUUsage {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.usage
}

func (m *CPUMonitor) CollectUsage(ctx context.Context, usage *CPUUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		file, err := os.Open(m.statPath)
		if err != nil {
			return err
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			// cpu should be the first line which is the summary of all procs
			if fields[0] == "cpu" {
				// skip the first field which is "cpu"
				return parseCPUStats(fields[1:], usage)
			}
		}
		return fmt.Errorf("cpu stats not found in /proc/stat")
	}
}

func (m *CPUMonitor) sync(ctx context.Context, usage *CPUUsage) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCPUSyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		usage.err = err
	}

	m.update(usage)

	m.ensureAlerts()

}

func (m *CPUMonitor) update(usage *CPUUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
}

func (m *CPUMonitor) getInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
}

func (m *CPUMonitor) setInterval(interval time.Duration) bool {
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

func (m *CPUMonitor) deleteAlert(severity v1alpha1.ResourceAlertSeverityType) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.alerts, severity)
}

func (m *CPUMonitor) setAlert(severity v1alpha1.ResourceAlertSeverityType, alert *Alert) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts[severity] = alert
}

func (m *CPUMonitor) clearAlerts() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.alerts = make(map[v1alpha1.ResourceAlertSeverityType]*Alert)
}

func (m *CPUMonitor) ensureAlerts() {
	for _, alert := range m.alerts {
		if m.updateAlert(alert) {
			m.log.Infof("Alert %s is firing", alert.Severity)
		}
	}
}

func (m *CPUMonitor) updateAlert(alert *Alert) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

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

func (m *CPUMonitor) Error() error {
	return m.usage.err
}

// ref. https://stackoverflow.com/questions/23367857/accurate-calculation-of-cpu-usage-given-in-percentage-in-linux
func parseCPUStats(fields []string, cpuUsage *CPUUsage) error {
	if len(fields) < 10 {
		return fmt.Errorf("invalid number of fields in cpu stats: %d", len(fields))
	}
	var err error
	cpuUsage.User, err = strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return err
	}
	cpuUsage.Nice, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return err
	}
	cpuUsage.System, err = strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return err
	}
	cpuUsage.Idle, err = strconv.ParseFloat(fields[3], 64)
	if err != nil {
		return err
	}
	cpuUsage.Iowait, err = strconv.ParseFloat(fields[4], 64)
	if err != nil {
		return err
	}
	cpuUsage.Irq, err = strconv.ParseFloat(fields[5], 64)
	if err != nil {
		return err
	}
	cpuUsage.Softirq, err = strconv.ParseFloat(fields[6], 64)
	if err != nil {
		return err
	}
	cpuUsage.Steal, err = strconv.ParseFloat(fields[7], 64)
	if err != nil {
		return err
	}
	cpuUsage.Guest, err = strconv.ParseFloat(fields[8], 64)
	if err != nil {
		return err
	}
	cpuUsage.GuestNice, err = strconv.ParseFloat(fields[9], 64)
	if err != nil {
		return err
	}

	// total CPU time as the sum of user, system, and idle times
	totalTime := cpuUsage.User + cpuUsage.Nice + cpuUsage.System + cpuUsage.Idle + cpuUsage.Iowait + cpuUsage.Irq + cpuUsage.Softirq + cpuUsage.Steal + cpuUsage.Guest + cpuUsage.GuestNice
	activeTime := totalTime - cpuUsage.Idle - cpuUsage.Iowait

	cpuUsage.UsedPercent = calculateCPUPercentage(activeTime, totalTime)
	cpuUsage.lastCollectedAt = time.Now()

	return nil
}

func calculateCPUPercentage(value, total float64) int64 {
	if total == 0 {
		return 0
	}
	return int64(math.Round(value / total * 100))
}

// CPUUsage represents the tracked CPU usage of this device.
type CPUUsage struct {
	// CPU // 0 skipped
	User      float64 // 1
	Nice      float64 // 2
	System    float64 // 3
	Idle      float64 // 4
	Iowait    float64 // 5
	Irq       float64 // 6
	Softirq   float64 // 7
	Steal     float64 // 8
	Guest     float64 // 9
	GuestNice float64 // 10

	UsedPercent     int64
	lastCollectedAt time.Time
	err             error
}

func (u *CPUUsage) Error() error {
	return u.err
}
