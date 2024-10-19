package resource

import (
	"context"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/ccoveille/go-safecast"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	DefaultDiskSyncTimeout = 5 * time.Second
)

var _ Monitor[DiskUsage] = (*DiskMonitor)(nil)

type DiskMonitor struct {
	mu     sync.Mutex
	alerts map[v1alpha1.ResourceAlertSeverityType]*Alert
	path   string

	updateIntervalCh chan time.Duration
	samplingInterval time.Duration

	log *log.PrefixLogger
}

func NewDiskMonitor(
	log *log.PrefixLogger,
) *DiskMonitor {
	return &DiskMonitor{
		alerts:           make(map[v1alpha1.ResourceAlertSeverityType]*Alert),
		updateIntervalCh: make(chan time.Duration, 1),
		samplingInterval: DefaultSamplingInterval,
		log:              log,
	}
}

func (m *DiskMonitor) Run(ctx context.Context) {
	defer m.log.Infof("Disk monitor stopped")
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
			m.log.Debug("Checking disk usage")
			usage := DiskUsage{}
			m.sync(ctx, &usage)
		}
	}
}

func (m *DiskMonitor) Update(monitor *v1alpha1.ResourceMonitor) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	spec, err := getMonitorSpec(monitor)
	if err != nil {
		return false, err
	}

	updated, err := updateMonitor(m.log, monitor, &m.samplingInterval, m.alerts, m.updateIntervalCh)
	if err != nil {
		return updated, err
	}

	if spec.Path != m.path {
		m.path = spec.Path
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

func (m *DiskMonitor) CollectUsage(ctx context.Context, usage *DiskUsage) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		path := m.getPath()
		diskInfo, err := getDirUsage(path)
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

func (m *DiskMonitor) getPath() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.path
}

func (m *DiskMonitor) sync(ctx context.Context, usage *DiskUsage) {
	if !m.hasAlertRules() {
		m.log.Debug("Skipping disk usage sync as there are no alert rules")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, DefaultDiskSyncTimeout)
	defer cancel()

	if err := m.CollectUsage(ctx, usage); err != nil {
		m.log.Errorf("Failed to collect Disk usage: %v", err)
		return
	}

	m.ensureAlerts(usage.UsedPercent)
}

func (m *DiskMonitor) ensureAlerts(percentageUsed int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, alert := range m.alerts {
		alert.Sync(percentageUsed)
	}
}

func (m *DiskMonitor) hasAlertRules() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.alerts) > 0
}

func (m *DiskMonitor) getSamplingInterval() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.samplingInterval
}

// DiskUsage represents the tracked Disk usage of this device.
type DiskUsage struct {
	Inodes      uint64
	Total       uint64
	Free        uint64
	Used        uint64
	UsedPercent int64

	lastCollectedAt time.Time
}

func getDirUsage(dir string) (*DiskUsage, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(dir, &stat)
	if err != nil {
		return nil, err
	}

	bsize, err := safecast.ToUint64(stat.Bsize)
	if err != nil {
		return nil, err
	}

	return &DiskUsage{
		Inodes: stat.Files,
		Total:  stat.Blocks * bsize,
		Free:   stat.Bavail * bsize,
		Used:   (stat.Blocks - stat.Bfree) * bsize,
	}, nil
}

func percentageDiskUsed(free, total uint64) int64 {
	if total == 0 {
		return 0
	}
	percentage := (1 - (float64(free) / float64(total))) * 100
	return int64(math.Round(percentage))
}
