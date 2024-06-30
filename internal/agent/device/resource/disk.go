package resource

import (
	"context"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

var _ Monitor[DiskUsage] = (*DiskMonitor)(nil)

type DiskMonitor struct {
	mu                         sync.Mutex
	syncDuration               time.Duration
	syncTimeout                time.Duration
	diskPaths                  []string
	warnFreeCapacityThreshold  int64
	alertFreeCapacityThreshold int64
	usage                      *DiskUsage
	log                        *log.PrefixLogger
}

func NewDiskMonitor(
	log *log.PrefixLogger,
	alertFreeCapacityThreshold int64,
	warnFreeCapacityThreshold int64,
	syncDuration,
	syncTimeout time.Duration,
	paths []string,
) *DiskMonitor {
	return &DiskMonitor{
		alertFreeCapacityThreshold: alertFreeCapacityThreshold,
		warnFreeCapacityThreshold:  warnFreeCapacityThreshold,
		syncDuration:               syncDuration,
		syncTimeout:                syncTimeout,
		diskPaths:                  paths,
		log:                        log,
		usage:                      &DiskUsage{},
	}
}

func (m *DiskMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.syncDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			usage := DiskUsage{
				warnFreeCapacityThreshold:  m.warnFreeCapacityThreshold,
				alertFreeCapacityThreshold: m.alertFreeCapacityThreshold,
			}
			m.sync(ctx, &usage)
			m.update(&usage)
		}
	}
}

func (m *DiskMonitor) Usage() *DiskUsage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.usage
}

func (m *DiskMonitor) sync(ctx context.Context, usage *DiskUsage) {
	ctx, cancel := context.WithTimeout(ctx, m.syncTimeout)
	defer cancel()

	for _, path := range m.diskPaths {
		select {
		case <-ctx.Done():
			usage.err = ctx.Err()
			return
		default:
			diskInfo, err := getDirUsage(path)
			if err != nil {
				usage.err = err
				return
			}
			usage.Total += diskInfo.Total
			usage.Free += diskInfo.Free
			usage.Used += diskInfo.Used
		}
	}

	usage.AvailablePercent = percentageAvailable(usage.Free, usage.Total)
}

func (m *DiskMonitor) update(usage *DiskUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
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

func percentageAvailable(free, total uint64) int64 {
	if total == 0 {
		return 0
	}
	percentage := (float64(free) / float64(total)) * 100
	return int64(math.Round(percentage))
}

type DiskUsage struct {
	Inodes           uint64
	Total            uint64
	Free             uint64
	Used             uint64
	AvailablePercent int64

	warnFreeCapacityThreshold  int64
	alertFreeCapacityThreshold int64
	err                        error
}

func (u *DiskUsage) IsAlert() bool {
	return u.AvailablePercent < u.alertFreeCapacityThreshold
}

func (u *DiskUsage) IsWarn() bool {
	return u.AvailablePercent < u.warnFreeCapacityThreshold
}

func (u *DiskUsage) Error() error {
	return u.err
}
