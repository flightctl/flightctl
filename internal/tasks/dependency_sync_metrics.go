package tasks

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// DependencySyncCollector implements prometheus.Collector for dependency sync metrics.
type DependencySyncCollector struct {
	cyclesTotal       *prometheus.CounterVec
	changesTotal      *prometheus.CounterVec
	probeErrorsTotal  *prometheus.CounterVec
	probeLatency      *prometheus.HistogramVec
	informerConnected prometheus.Gauge
}

func NewDependencySyncCollector() *DependencySyncCollector {
	return &DependencySyncCollector{
		cyclesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_dependency_sync_cycles_total",
			Help: "Total number of dependency sync probe cycles by ref type.",
		}, []string{"ref_type"}),
		changesTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_dependency_sync_changes_total",
			Help: "Total number of dependency changes detected by ref type.",
		}, []string{"ref_type"}),
		probeErrorsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_dependency_sync_probe_errors_total",
			Help: "Total number of dependency sync probe errors by ref type.",
		}, []string{"ref_type"}),
		probeLatency: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "flightctl_dependency_sync_probe_latency_seconds",
			Help:    "Histogram of dependency sync probe latency by ref type.",
			Buckets: prometheus.ExponentialBuckets(0.01, 2, 12), // 10ms .. ~20s
		}, []string{"ref_type"}),
		informerConnected: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_dependency_sync_informer_connected",
			Help: "Whether the Kubernetes secret informer is connected (1) or disconnected (0).",
		}),
	}
}

func (c *DependencySyncCollector) Describe(ch chan<- *prometheus.Desc) {
	c.cyclesTotal.Describe(ch)
	c.changesTotal.Describe(ch)
	c.probeErrorsTotal.Describe(ch)
	c.probeLatency.Describe(ch)
	c.informerConnected.Describe(ch)
}

func (c *DependencySyncCollector) Collect(ch chan<- prometheus.Metric) {
	c.cyclesTotal.Collect(ch)
	c.changesTotal.Collect(ch)
	c.probeErrorsTotal.Collect(ch)
	c.probeLatency.Collect(ch)
	c.informerConnected.Collect(ch)
}

func (c *DependencySyncCollector) ObserveProbeCycle(refType string) {
	c.cyclesTotal.WithLabelValues(refType).Inc()
}

func (c *DependencySyncCollector) ObserveProbeChange(refType string) {
	c.changesTotal.WithLabelValues(refType).Inc()
}

func (c *DependencySyncCollector) ObserveProbeError(refType string) {
	c.probeErrorsTotal.WithLabelValues(refType).Inc()
}

func (c *DependencySyncCollector) ObserveProbeLatency(refType string, d time.Duration) {
	c.probeLatency.WithLabelValues(refType).Observe(d.Seconds())
}

func (c *DependencySyncCollector) SetInformerConnected(connected bool) {
	if connected {
		c.informerConnected.Set(1)
	} else {
		c.informerConnected.Set(0)
	}
}
