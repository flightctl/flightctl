package resource

import (
	"context"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/flightctl/flightctl/pkg/log"
)

type Manager interface {
	Usage() (*Usage, error)
	Run(ctx context.Context)
}

type Collector[T any] interface {
	Usage() *T
	Run(ctx context.Context)
}

type ResourceManager struct {
	filesystem Collector[FsUsage]
	log        *log.PrefixLogger
}

// NewManager creates a new resource Manager.
func NewManager(
	log *log.PrefixLogger,
	fsAlertThreshold int,
	fsWarnThreshold int,
	fsPaths []string,
	fsSyncDuration time.Duration,
	fsSyncTimeout time.Duration,
) Manager {
	return &ResourceManager{
		filesystem: NewFs(
			log,
			fsAlertThreshold,
			fsWarnThreshold,
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

	go m.filesystem.Run(ctx)

	<-ctx.Done()
}

func (m *ResourceManager) Usage() (*Usage, error) {
	return &Usage{
		FsUsage: m.filesystem.Usage(),
	}, nil
}

type Usage struct {
	FsUsage *FsUsage
}

type FsUsage struct {
	alertThreshold int
	warnThreshold  int
	Inodes         uint64
	Total          uint64
	Free           uint64
	Used           uint64
}

func (u *FsUsage) IsAlert() bool {
	avail := percentageAvailable(u.Free, u.Total)
	return avail < u.alertThreshold
}

func (u *FsUsage) IsWarn() bool {
	avail := percentageAvailable(u.Free, u.Total)
	return avail < u.warnThreshold
}

var _ Collector[FsUsage] = (*Fs)(nil)

type Fs struct {
	mu           sync.Mutex
	syncDuration time.Duration
	syncTimeout  time.Duration
	fsPaths      []string
	usage        FsUsage
	log          *log.PrefixLogger
}

func NewFs(
	log *log.PrefixLogger,
	alertPercentThreshold int,
	warnPercentThreshold int,
	syncDuration,
	syncTimeout time.Duration,
	paths []string,
) *Fs {
	return &Fs{
		syncDuration: syncDuration,
		syncTimeout:  syncTimeout,
		fsPaths:      paths,
		usage: FsUsage{
			alertThreshold: alertPercentThreshold,
			warnThreshold:  warnPercentThreshold,
		},
		log: log,
	}
}

func (f *Fs) Run(ctx context.Context) {
	ticker := time.NewTicker(f.syncDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := f.sync(ctx); err != nil {
				f.log.Errorf("Error syncing filesystem usage: %v", err)
			}
		}
	}
}

func (f *Fs) Usage() *FsUsage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &f.usage
}

func (f *Fs) sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, f.syncTimeout)
	defer cancel()

	for i, path := range f.fsPaths {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			fsInfo, err := f.getDirUsage(path)
			if err != nil {
				return err
			}
			// base dir is the first path
			if i == 0 {
				f.update(fsInfo, false)
			} else {
				f.update(fsInfo, true)
			}
		}
	}

	return nil
}

func (f *Fs) update(usage *FsUsage, append bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if append {
		f.usage.Total += usage.Total
		f.usage.Free += usage.Free
		f.usage.Used += usage.Used
	} else {
		f.usage.Total = usage.Total
		f.usage.Free = usage.Free
		f.usage.Used = usage.Used
	}
}

func (fs *Fs) getDirUsage(dir string) (*FsUsage, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(dir, &stat)
	if err != nil {
		return nil, err
	}

	return &FsUsage{
		Inodes: stat.Files,
		Total:  stat.Blocks * uint64(stat.Bsize),
		Free:   stat.Bavail * uint64(stat.Bsize),
		Used:   (stat.Blocks - stat.Bfree) * uint64(stat.Bsize),
	}, nil
}

func percentageAvailable(free, total uint64) int {
	if total == 0 {
		return 0
	}
	percentage := (float64(free) / float64(total)) * 100
	return int(math.Round(percentage))
}
