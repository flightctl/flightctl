package metrics

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/mackerelio/go-osstat/memory"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sys/unix"
)

// SystemCollector implements NamedCollector and gathers system resource usage like CPU, memory, and disk.
type SystemCollector struct {
	cpuGauge  prometheus.Gauge
	memGauge  prometheus.Gauge
	diskGauge prometheus.Gauge

	lastIdle       uint64
	lastTotal      uint64
	mu             sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
	tickerInterval time.Duration
}

func NewSystemCollector(ctx context.Context, cfg *config.Config) *SystemCollector {
	interval := cfg.Metrics.SystemCollector.TickerInterval

	c, cancel := context.WithCancel(ctx)
	collector := &SystemCollector{
		cpuGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_cpu_utilization",
			Help: "Flightctl CPU utilization",
		}),
		memGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_memory_utilization",
			Help: "Flightctl memory utilization",
		}),
		diskGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_disk_utilization",
			Help: "Flightctl storage utilization",
		}),
		ctx:            c,
		cancel:         cancel,
		tickerInterval: time.Duration(interval),
	}

	go collector.sampleCPU()
	go collector.sampleMemory()
	go collector.sampleDisk()

	return collector
}

func (c *SystemCollector) MetricsName() string {
	return "system"
}

func (c *SystemCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cpuGauge.Desc()
	ch <- c.memGauge.Desc()
	ch <- c.diskGauge.Desc()
}

func (c *SystemCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ch <- c.cpuGauge
	ch <- c.memGauge
	ch <- c.diskGauge
}

func (c *SystemCollector) sampleCPU() {
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			stats, err := cpu.Get()
			if err != nil {
				continue
			}

			if c.lastTotal != 0 {
				deltaIdle := stats.Idle - c.lastIdle
				deltaTotal := stats.Total - c.lastTotal
				if deltaTotal > 0 {
					usage := 1.0 - float64(deltaIdle)/float64(deltaTotal)
					c.mu.Lock()
					c.cpuGauge.Set(usage)
					c.mu.Unlock()
				}
			}

			c.lastIdle = stats.Idle
			c.lastTotal = stats.Total
		}
	}
}

func (c *SystemCollector) sampleMemory() {
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			stats, err := memory.Get()
			if err != nil || stats.Total == 0 {
				continue
			}

			used := float64(stats.Used) / float64(stats.Total)
			c.mu.Lock()
			c.memGauge.Set(used)
			c.mu.Unlock()
		}
	}
}

func (c *SystemCollector) sampleDisk() {
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	var stat unix.Statfs_t

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			err := unix.Statfs("/", &stat)
			if err != nil || stat.Blocks == 0 {
				continue
			}

			used := 1.0 - float64(stat.Bfree)/float64(stat.Blocks)
			c.mu.Lock()
			c.diskGauge.Set(used)
			c.mu.Unlock()
		}
	}
}
