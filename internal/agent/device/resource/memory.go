package resource

import (
	"context"
	"os"
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
	mu          sync.Mutex
	alerts      map[v1alpha1.ResourceAlertSeverityType]*Alert
	memInfoPath string

	updateIntervalCh chan time.Duration
	samplingInterval time.Duration

	log *log.PrefixLogger
}

func NewMemoryMonitor(
	log *log.PrefixLogger,
) *MemoryMonitor {
	return &MemoryMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		samplingInterval: DefaultSamplingInterval,
		memInfoPath:      DefaultProcMemInfoPath,
		log:              log,
	}
}

func (m *MemoryMonitor) Run(ctx context.Context) {
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
			m.log.Debug("Checking memory usage")
			usage := MemoryUsage{}
			m.sync(ctx, &usage)
		}
	}
}

func (m *MemoryMonitor) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	return updateMonitor(m.log, monitor, &m.samplingInterval, m.alerts, m.updateIntervalCh)
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
	if !m.hasAlertRules() {
		m.log.Debug("Skipping memory usage sync: no alert rules")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultMemorySyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		m.log.Errorf("Failed to collect Memory usage: %v", err)
	}

	m.ensureAlerts(usage.UsedPercent)
}

func (m *MemoryMonitor) ensureAlerts(percentageUsed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.log.Tracef("Memory usage: %d%%", percentageUsed)
	for _, alert := range m.alerts {
		alert.Sync(percentageUsed)
	}
}

func (m *MemoryMonitor) hasAlertRules() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts) > 0
}

func (m *MemoryMonitor) getSamplingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
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
}
