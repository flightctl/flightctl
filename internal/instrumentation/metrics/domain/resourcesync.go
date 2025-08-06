package domain

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// ResourceSyncCollector implements NamedCollector and gathers resourcesync-related domain metrics.
type ResourceSyncCollector struct {
	resourceSyncsGauge *prometheus.GaugeVec

	store          store.Store
	log            logrus.FieldLogger
	mu             sync.RWMutex
	ctx            context.Context
	tickerInterval time.Duration
	cfg            *config.Config
}

// NewResourceSyncCollector creates a ResourceSyncCollector. If tickerInterval is 0, defaults to 30s.
func NewResourceSyncCollector(ctx context.Context, store store.Store, log logrus.FieldLogger, cfg *config.Config, tickerInterval ...time.Duration) *ResourceSyncCollector {
	interval := cfg.Metrics.ResourceSyncCollector.TickerInterval

	collector := &ResourceSyncCollector{
		resourceSyncsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_resourcesyncs",
			Help: "Total number of resource syncs managed",
		}, []string{"organization_id", "status"}),
		store:          store,
		log:            log,
		ctx:            ctx,
		tickerInterval: time.Duration(interval),
		cfg:            cfg,
	}

	collector.log.Info("Starting resourcesync metrics collector with interval", "interval", interval)
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
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	c.log.Info("ResourceSync metrics collector sampling started")
	for {
		select {
		case <-c.ctx.Done():
			c.log.Info("ResourceSync metrics collector context cancelled, stopping")
			return
		case <-ticker.C:
			c.log.Debug("Collecting resourcesync metrics")
			c.updateResourceSyncMetrics()
		}
	}
}

func (c *ResourceSyncCollector) updateResourceSyncMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	// Get resource sync counts grouped by org and status
	resourceSyncCounts, err := c.store.ResourceSync().CountByOrgAndStatus(ctx, nil, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get resource sync counts for metrics")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update resource syncs metric
	c.resourceSyncsGauge.Reset()
	for _, result := range resourceSyncCounts {
		orgIdLabel := result.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		status := result.Status
		if status == "" {
			status = "none"
		}
		c.resourceSyncsGauge.WithLabelValues(orgIdLabel, status).Set(float64(result.Count))
	}

	c.log.WithFields(logrus.Fields{
		"org_count": len(resourceSyncCounts),
	}).Debug("Updated resourcesync metrics")
}
