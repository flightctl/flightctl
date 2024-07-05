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

	"github.com/flightctl/flightctl/pkg/log"
)

var _ Monitor[CPUUsage] = (*CPUMonitor)(nil)

type CPUMonitor struct {
	mu                         sync.Mutex
	syncDuration               time.Duration
	syncTimeout                time.Duration
	alertFreeCapacityThreshold int64
	warnFreeCapacityThreshold  int64
	usage                      *CPUUsage
	log                        *log.PrefixLogger
}

func NewCPUMonitor(
	log *log.PrefixLogger,
	alertFreeCapacityThreshold int64,
	warnFreeCapacityThreshold int64,
	syncDuration,
	syncTimeout time.Duration,
) *CPUMonitor {
	return &CPUMonitor{
		alertFreeCapacityThreshold: alertFreeCapacityThreshold,
		warnFreeCapacityThreshold:  warnFreeCapacityThreshold,
		syncDuration:               syncDuration,
		syncTimeout:                syncTimeout,
		usage:                      &CPUUsage{},
		log:                        log,
	}
}

func (m *CPUMonitor) Run(ctx context.Context) {
	ticker := time.NewTicker(m.syncDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cpuUsage := &CPUUsage{
				alertFreeCapacityThreshold: m.alertFreeCapacityThreshold,
				warnFreeCapacityThreshold:  m.warnFreeCapacityThreshold,
			}
			m.sync(ctx, cpuUsage)
			m.update(cpuUsage)
		}
	}
}

func (m *CPUMonitor) Usage() *CPUUsage {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.usage
}

func (m *CPUMonitor) update(usage *CPUUsage) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.usage = usage
}

func (m *CPUMonitor) sync(ctx context.Context, cpuUsage *CPUUsage) {
	ctx, cancel := context.WithTimeout(ctx, m.syncTimeout)
	defer cancel()

	select {
	case <-ctx.Done():
		cpuUsage.err = ctx.Err()
		return
	default:
		file, err := os.Open("/proc/stat")
		if err != nil {
			cpuUsage.err = err
			return
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			// cpu should be the first line which is the summary of all procs
			if fields[0] == "cpu" {
				// skip the first field which is "cpu"
				parseCPUStats(fields[1:], cpuUsage)
				return
			}
		}
		cpuUsage.err = fmt.Errorf("cpu stats not found in /proc/stat")
	}
}

func parseCPUStats(fields []string, cpuUsage *CPUUsage) {
	if len(fields) < 5 {
		cpuUsage.err = fmt.Errorf("invalid number of fields in cpu stats: %d", len(fields))
		return
	}
	var err error
	cpuUsage.User, err = strconv.ParseFloat(fields[1], 64)
	if err != nil {
		cpuUsage.err = err
		return
	}
	cpuUsage.System, err = strconv.ParseFloat(fields[3], 64)
	if err != nil {
		cpuUsage.err = err
		return
	}
	cpuUsage.Idle, err = strconv.ParseFloat(fields[4], 64)
	if err != nil {
		cpuUsage.err = err
		return
	}

	// total CPU time as the sum of user, system, and idle times
	totalTime := cpuUsage.User + cpuUsage.System + cpuUsage.Idle

	// Percentage of CPU time that is idle (Idle / Total) * 100
	cpuUsage.AvailablePercent = calculateCPUPercentage(cpuUsage.Idle, totalTime)
}

func calculateCPUPercentage(value, total float64) int64 {
	if total == 0 {
		return 0
	}
	return int64(math.Round((value/total*100)*100) / 100)
}

type CPUUsage struct {
	// CPU	// 0
	User, // 1
	System, // 3
	Idle float64 // 4
	AvailablePercent int64

	alertFreeCapacityThreshold int64
	warnFreeCapacityThreshold  int64
	err                        error
}

func (u *CPUUsage) IsAlert() bool {
	return u.AvailablePercent < u.alertFreeCapacityThreshold
}

func (u *CPUUsage) IsWarn() bool {
	return u.AvailablePercent < u.warnFreeCapacityThreshold
}

func (u *CPUUsage) Error() error {
	return u.err
}
