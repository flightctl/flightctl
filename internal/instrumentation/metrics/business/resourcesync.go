package business

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// ResourceSyncCollector implements NamedCollector and gathers resourcesync-related business metrics.
type ResourceSyncCollector struct {
	resourceSyncsGauge *prometheus.GaugeVec

	store store.Store
	log   logrus.FieldLogger
	mu    sync.RWMutex
	ctx   context.Context
}

func NewResourceSyncCollector(ctx context.Context, store store.Store, log logrus.FieldLogger) *ResourceSyncCollector {
	collector := &ResourceSyncCollector{
		resourceSyncsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_resourcesyncs_total",
			Help: "Total number of resource syncs managed",
		}, []string{"organization_id", "status", "version"}),

		store: store,
		log:   log,
		ctx:   ctx,
	}

	collector.updateResourceSyncMetrics() // immediate update
	go collector.sampleResourceSyncMetrics()

	return collector
}

func (c *ResourceSyncCollector) MetricsName() string {
	return "resourcesync"
}

func (c *ResourceSyncCollector) Describe(ch chan<- *prometheus.Desc) {
	c.resourceSyncsGauge.Describe(ch)
}

func (c *ResourceSyncCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	c.resourceSyncsGauge.Collect(ch)
}

func (c *ResourceSyncCollector) sampleResourceSyncMetrics() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateResourceSyncMetrics()
		}
	}
}

func (c *ResourceSyncCollector) updateResourceSyncMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset all metrics
	c.resourceSyncsGauge.Reset()

	// Get resource sync counts grouped by organization, status, and version
	results, err := c.store.ResourceSync().CountByOrgStatusAndVersion(ctx, nil, nil, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get resource sync count for metrics")
		return
	}

	// Update metrics with actual organization, status, and version values
	for _, r := range results {
		orgIdLabel := r.OrgID
		statusLabel := r.Status
		versionLabel := r.Version
		c.resourceSyncsGauge.WithLabelValues(orgIdLabel, statusLabel, versionLabel).Set(float64(r.Count))
	}
}
