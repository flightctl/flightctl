package resource

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	Usage() *Usage
	Run(ctx context.Context)
}

type Monitor[T any] interface {
	Usage() *T
	Run(ctx context.Context)
}

type ResourceManager struct {
	diskMonitor Monitor[DiskUsage]
	cpuMonitor  Monitor[CPUUsage]
	log         *log.PrefixLogger
}

// NewManager creates a new resource Manager.
func NewManager(
	log *log.PrefixLogger,
	diskAlertFreeCapacityThreshold int64,
	diskWarnFreeCapacityThreshold int64,
	diskPaths []string,
	diskSyncDuration time.Duration,
	diskSyncTimeout time.Duration,

	cpuAlertFreeCapacityThreshold int64,
	cpiWarnFreeCapacityThreshold int64,
	cpuSyncDuration time.Duration,
	cpuSyncTimeout time.Duration,
) Manager {
	return &ResourceManager{
		diskMonitor: NewDiskMonitor(
			log,
			diskAlertFreeCapacityThreshold,
			diskWarnFreeCapacityThreshold,
			diskSyncDuration,
			diskSyncTimeout,
			diskPaths,
		),
		cpuMonitor: NewCPUMonitor(
			log,
			cpuAlertFreeCapacityThreshold,
			cpiWarnFreeCapacityThreshold,
			cpuSyncDuration,
			cpuSyncTimeout,
		),
		log: log,
	}
}

func (m *ResourceManager) Run(ctx context.Context) {
	m.log.Infof("Starting resource manager...")
	defer m.log.Infof("Resource manager stopped")

	go m.diskMonitor.Run(ctx)
	m.cpuMonitor.Run(ctx)

}

func (m *ResourceManager) Usage() *Usage {
	return &Usage{
		DiskUsage: m.diskMonitor.Usage(),
		CPUUsage:  m.cpuMonitor.Usage(),
	}
}

type Usage struct {
	DiskUsage *DiskUsage
	CPUUsage  *CPUUsage
}
