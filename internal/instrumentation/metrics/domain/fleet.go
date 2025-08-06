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

// FleetCollector implements NamedCollector and gathers fleet-related domain metrics.
type FleetCollector struct {
	totalFleetsGauge *prometheus.GaugeVec

	store          store.Store
	log            logrus.FieldLogger
	mu             sync.RWMutex
	ctx            context.Context
	tickerInterval time.Duration
	cfg            *config.Config
}

// NewFleetCollector creates a FleetCollector. If tickerInterval is 0, defaults to 30s.
func NewFleetCollector(ctx context.Context, store store.Store, log logrus.FieldLogger, cfg *config.Config) *FleetCollector {
	interval := cfg.Metrics.FleetCollector.TickerInterval

	collector := &FleetCollector{
		totalFleetsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_fleets",
			Help: "Total number of fleets managed (by status)",
		}, []string{"organization_id", "status"}),
		store:          store,
		log:            log,
		ctx:            ctx,
		tickerInterval: time.Duration(interval),
		cfg:            cfg,
	}

	collector.log.Info("Starting fleet metrics collector with interval", "interval", interval)
	collector.updateFleetMetrics() // immediate update
	go collector.sampleFleetMetrics()

	return collector
}

func (c *FleetCollector) MetricsName() string {
	return "fleet"
}

func (c *FleetCollector) Describe(ch chan<- *prometheus.Desc) {
	c.totalFleetsGauge.Describe(ch)
}

func (c *FleetCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.totalFleetsGauge.Collect(ch)
}

func (c *FleetCollector) sampleFleetMetrics() {
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	c.log.Info("Fleet metrics collector sampling started")
	for {
		select {
		case <-c.ctx.Done():
			c.log.Info("Fleet metrics collector context cancelled, stopping")
			return
		case <-ticker.C:
			c.log.Debug("Collecting fleet metrics")
			c.updateFleetMetrics()
		}
	}
}

func (c *FleetCollector) updateFleetMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	// Get status counts grouped by org and status
	statusCounts, err := c.store.Fleet().CountByRolloutStatus(ctx, nil, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get fleet status counts for metrics")
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update total fleets metric
	c.totalFleetsGauge.Reset()
	for _, result := range statusCounts {
		orgIdLabel := result.OrgID
		status := result.Status
		if status == "" {
			status = "none"
		}
		c.totalFleetsGauge.WithLabelValues(orgIdLabel, status).Set(float64(result.Count))
	}

	c.log.WithFields(logrus.Fields{
		"org_count": len(statusCounts),
	}).Debug("Updated fleet metrics")
}
