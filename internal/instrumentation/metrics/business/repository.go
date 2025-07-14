package business

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// RepositoryCollector implements NamedCollector and gathers repository-related business metrics.
type RepositoryCollector struct {
	repositoriesGauge *prometheus.GaugeVec

	store store.Store
	log   logrus.FieldLogger
	mu    sync.RWMutex
	ctx   context.Context
}

func NewRepositoryCollector(ctx context.Context, store store.Store, log logrus.FieldLogger) *RepositoryCollector {
	collector := &RepositoryCollector{
		repositoriesGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_repositories_total",
			Help: "Total number of repositories managed",
		}, []string{"organization_id", "version"}),

		store: store,
		log:   log,
		ctx:   ctx,
	}

	collector.updateRepositoryMetrics() // immediate update
	go collector.sampleRepositoryMetrics()

	return collector
}

func (c *RepositoryCollector) MetricsName() string {
	return "repository"
}

func (c *RepositoryCollector) Describe(ch chan<- *prometheus.Desc) {
	c.repositoriesGauge.Describe(ch)
}

func (c *RepositoryCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.repositoriesGauge.Collect(ch)
}

func (c *RepositoryCollector) sampleRepositoryMetrics() {
	ticker := time.NewTicker(30 * time.Second) // Sample every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateRepositoryMetrics()
		}
	}
}

func (c *RepositoryCollector) updateRepositoryMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset all metrics
	c.repositoriesGauge.Reset()

	// Get repository counts grouped by organization and version
	results, err := c.store.Repository().CountByOrgAndVersion(ctx, nil, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get repository count for metrics")
		return
	}

	// Update metrics with actual organization and version values
	for _, r := range results {
		orgIdLabel := r.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		version := r.Version
		if version == "" {
			version = "unknown"
		}
		c.repositoriesGauge.WithLabelValues(orgIdLabel, version).Set(float64(r.Count))
	}

	c.log.WithFields(logrus.Fields{
		"repository_count": len(results),
	}).Debug("Updated repository metrics")
}
