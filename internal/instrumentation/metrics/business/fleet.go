package business

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

const defaultVersion = "unknown"

// FleetCollector implements NamedCollector and gathers fleet-related business metrics.
type FleetCollector struct {
	totalFleetsGauge        *prometheus.GaugeVec
	fleetRolloutStatusGauge *prometheus.GaugeVec

	store store.Store
	log   logrus.FieldLogger
	mu    sync.RWMutex
	ctx   context.Context
}

func NewFleetCollector(ctx context.Context, store store.Store, log logrus.FieldLogger) *FleetCollector {
	collector := &FleetCollector{
		totalFleetsGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_fleets_total",
			Help: "Total number of fleets managed",
		}, []string{"organization_id", "version"}),
		fleetRolloutStatusGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_fleet_rollout_status",
			Help: "Status of ongoing fleet rollouts",
		}, []string{"organization_id", "version", "status"}),
		store: store,
		log:   log,
		ctx:   ctx,
	}

	collector.updateFleetMetrics() // immediate update
	go collector.sampleFleetMetrics()

	return collector
}

func (c *FleetCollector) MetricsName() string {
	return "fleet"
}

func (c *FleetCollector) Describe(ch chan<- *prometheus.Desc) {
	c.totalFleetsGauge.Describe(ch)
	c.fleetRolloutStatusGauge.Describe(ch)
}

func (c *FleetCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.totalFleetsGauge.Collect(ch)
	c.fleetRolloutStatusGauge.Collect(ch)
}

func (c *FleetCollector) sampleFleetMetrics() {
	ticker := time.NewTicker(30 * time.Second) // Sample every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.updateFleetMetrics()
		}
	}
}

func (c *FleetCollector) updateFleetMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	// Pass nil to get all organizations and all versions
	// Get rollout status counts
	rolloutStatusCounts, err := c.store.Fleet().CountByRolloutStatus(ctx, nil, nil)
	if err != nil {
		c.log.WithError(err).Error("Failed to get fleet rollout status counts for metrics")
		return
	}

	// Aggregate by organization and version
	totals := map[string]map[string]int64{}                  // orgId -> version -> count
	statusCounts := map[string]map[string]map[string]int64{} // orgId -> version -> status -> count

	for _, result := range rolloutStatusCounts {
		if totals[result.OrgID] == nil {
			totals[result.OrgID] = make(map[string]int64)
		}
		if statusCounts[result.OrgID] == nil {
			statusCounts[result.OrgID] = make(map[string]map[string]int64)
		}
		if statusCounts[result.OrgID][result.Version] == nil {
			statusCounts[result.OrgID][result.Version] = make(map[string]int64)
		}

		totals[result.OrgID][result.Version] += result.Count
		status := result.Status
		if status == "" {
			status = "none"
		}
		statusCounts[result.OrgID][result.Version][status] = result.Count
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Update total fleets metric
	c.totalFleetsGauge.Reset()
	for _, result := range rolloutStatusCounts {
		orgIdLabel := result.OrgID
		version := result.Version
		total := totals[orgIdLabel][version]
		c.totalFleetsGauge.WithLabelValues(orgIdLabel, version).Set(float64(total))
	}

	// Update fleet rollout status metrics
	c.fleetRolloutStatusGauge.Reset()
	for _, result := range rolloutStatusCounts {
		status := result.Status
		if status == "" {
			status = "none"
		}
		orgIdLabel := result.OrgID
		version := result.Version
		c.fleetRolloutStatusGauge.WithLabelValues(orgIdLabel, version, status).Set(float64(result.Count))
	}

	c.log.WithFields(logrus.Fields{
		"org_count":    len(totals),
		"status_count": len(rolloutStatusCounts),
	}).Debug("Updated fleet metrics")
}
