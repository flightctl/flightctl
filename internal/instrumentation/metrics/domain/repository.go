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

// RepositoryCollector implements NamedCollector and gathers repository-related domain metrics.
type RepositoryCollector struct {
	repositoriesGauge *prometheus.GaugeVec

	store          store.Store
	log            logrus.FieldLogger
	mu             sync.RWMutex
	ctx            context.Context
	tickerInterval time.Duration
	cfg            *config.Config
}

// NewRepositoryCollector creates a RepositoryCollector. If tickerInterval is 0, defaults to 30s.
func NewRepositoryCollector(ctx context.Context, store store.Store, log logrus.FieldLogger, cfg *config.Config) *RepositoryCollector {
	interval := cfg.Metrics.RepositoryCollector.TickerInterval

	collector := &RepositoryCollector{
		repositoriesGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_repositories_total",
			Help: "Total number of repositories managed",
		}, []string{"organization_id"}),
		store:          store,
		log:            log,
		ctx:            ctx,
		tickerInterval: time.Duration(interval),
		cfg:            cfg,
	}

	collector.log.Info("Starting repository metrics collector with interval", "interval", interval)
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
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	c.log.Info("Repository metrics collector sampling started")
	for {
		select {
		case <-c.ctx.Done():
			c.log.Info("Repository metrics collector context cancelled, stopping")
			return
		case <-ticker.C:
			c.log.Debug("Collecting repository metrics")
			c.updateRepositoryMetrics()
		}
	}
}

func (c *RepositoryCollector) updateRepositoryMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	// Get repository counts grouped by org
	repoCounts, err := c.store.Repository().CountByOrg(ctx, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get repository counts for metrics")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update repositories metric
	c.repositoriesGauge.Reset()
	for _, result := range repoCounts {
		orgIdLabel := result.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		c.repositoriesGauge.WithLabelValues(orgIdLabel).Set(float64(result.Count))
	}

	c.log.WithFields(logrus.Fields{
		"org_count": len(repoCounts),
	}).Debug("Updated repository metrics")
}
