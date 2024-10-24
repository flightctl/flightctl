package resource

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
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
	mu       sync.Mutex
	alerts   map[v1alpha1.ResourceAlertSeverityType]*Alert
	statPath string

	updateIntervalCh chan time.Duration
	samplingInterval time.Duration

	log *log.PrefixLogger
}

func NewCPUMonitor(
	log *log.PrefixLogger,
) *CPUMonitor {
	return &CPUMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		statPath:         DefaultProcStatPath,
		updateIntervalCh: make(chan time.Duration, 1),
		samplingInterval: DefaultSamplingInterval,
		log:              log,
	}
}

func (m *CPUMonitor) Run(ctx context.Context) {
	defer m.log.Infof("CPU monitor stopped")
	samplingInterval := m.getSamplingInterval()
	ticker := time.NewTicker(samplingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case newInterval := <-m.updateIntervalCh:
			ticker.Reset(newInterval)
		case <-ticker.C:
			m.log.Debug("Checking CPU usage")
			usage := CPUUsage{}
			m.sync(ctx, &usage)
		}
	}
}

func (m *CPUMonitor) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return updateMonitor(m.log, monitor, &m.samplingInterval, m.alerts, m.updateIntervalCh)
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
	if !m.hasAlertRules() {
		m.log.Debug("Skipping CPU usage sync: no alert rules")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultCPUSyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		m.log.Errorf("Failed to collect CPU usage: %v", err)
		return
	}

	m.ensureAlerts(usage.UsedPercent)
}

func (m *CPUMonitor) ensureAlerts(percentageUsed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, alert := range m.alerts {
		alert.Sync(percentageUsed)
	}
}

func (m *CPUMonitor) hasAlertRules() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts) > 0
}

func (m *CPUMonitor) getSamplingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
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
