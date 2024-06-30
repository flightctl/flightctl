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
	log         *log.PrefixLogger
}

// NewManager creates a new resource Manager.
func NewManager(
	log *log.PrefixLogger,
	diskAlertFreeCapacityThreshold int,
	diskWarnFreeCapacityThreshold int,
	fsPaths []string,
	fsSyncDuration time.Duration,
	fsSyncTimeout time.Duration,
) Manager {
	return &ResourceManager{
		diskMonitor: NewDiskMonitor(
			log,
			diskAlertFreeCapacityThreshold,
			diskWarnFreeCapacityThreshold,
			fsSyncDuration,
			fsSyncTimeout,
			fsPaths,
		),
		log: log,
	}
}

func (m *ResourceManager) Run(ctx context.Context) {
	m.log.Infof("Starting resource manager...")
	defer m.log.Infof("Resource manager stopped")

	m.diskMonitor.Run(ctx)
}

func (m *ResourceManager) Usage() *Usage {
	return &Usage{
		DiskUsage: m.diskMonitor.Usage(),
	}
}

type Usage struct {
	DiskUsage *DiskUsage
}
