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

// DeviceCollector implements NamedCollector and gathers device-related domain metrics.
type DeviceCollector struct {
	// Summary status metrics
	devicesSummaryGauge *prometheus.GaugeVec

	// Application status metrics
	devicesApplicationGauge *prometheus.GaugeVec

	// System update status metrics
	devicesUpdateGauge *prometheus.GaugeVec

	store          store.Store
	log            logrus.FieldLogger
	mu             sync.RWMutex
	ctx            context.Context
	tickerInterval time.Duration
	cfg            *config.Config
}

// NewDeviceCollector creates a DeviceCollector. If tickerInterval is 0, defaults to 30s.
func NewDeviceCollector(ctx context.Context, store store.Store, log logrus.FieldLogger, cfg *config.Config) *DeviceCollector {
	interval := cfg.Metrics.DeviceCollector.TickerInterval

	collector := &DeviceCollector{
		// Summary status metrics
		devicesSummaryGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_summary",
			Help: "Total number of devices managed (by summary status)",
		}, []string{"organization_id", "fleet", "status"}),

		// Application status metrics
		devicesApplicationGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_application",
			Help: "Total number of devices managed (by application status)",
		}, []string{"organization_id", "fleet", "status"}),

		// System update status metrics
		devicesUpdateGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_update",
			Help: "Total number of devices managed (by system update status)",
		}, []string{"organization_id", "fleet", "status"}),

		store:          store,
		log:            log,
		ctx:            ctx,
		tickerInterval: time.Duration(interval),
		cfg:            cfg,
	}

	collector.log.Info("Starting device metrics collector with interval", "interval", interval)
	collector.updateDeviceMetrics() // immediate update
	go collector.sampleDeviceMetrics()

	return collector
}

func (c *DeviceCollector) MetricsName() string {
	return "device"
}

func (c *DeviceCollector) Describe(ch chan<- *prometheus.Desc) {
	c.devicesSummaryGauge.Describe(ch)
	c.devicesApplicationGauge.Describe(ch)
	c.devicesUpdateGauge.Describe(ch)
}

func (c *DeviceCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.devicesSummaryGauge.Collect(ch)
	c.devicesApplicationGauge.Collect(ch)
	c.devicesUpdateGauge.Collect(ch)
}

func (c *DeviceCollector) sampleDeviceMetrics() {
	ticker := time.NewTicker(c.tickerInterval)
	defer ticker.Stop()

	c.log.Info("Device metrics collector sampling started")
	for {
		select {
		case <-c.ctx.Done():
			c.log.Info("Device metrics collector context cancelled, stopping")
			return
		case <-ticker.C:
			c.log.Debug("Collecting device metrics")
			c.updateDeviceMetrics()
		}
	}
}

func (c *DeviceCollector) updateDeviceMetrics() {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()

	// Use bypass span check for metrics collection to avoid tracing context errors
	ctx = store.WithBypassSpanCheck(ctx)

	c.mu.Lock()
	defer c.mu.Unlock()

	// Reset all metrics
	c.devicesSummaryGauge.Reset()
	c.devicesApplicationGauge.Reset()
	c.devicesUpdateGauge.Reset()

	// Update summary status metrics
	summaryResults, err := c.store.Device().CountByOrgAndStatus(ctx, nil, store.DeviceStatusTypeSummary, c.cfg.Metrics.DeviceCollector.GroupByFleet)
	if err != nil {
		c.log.WithError(err).Error("Failed to get device summary status counts")
		return
	}

	// Update summary metrics with actual status values
	for _, r := range summaryResults {
		orgIdLabel := r.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		fleetLabel := r.Fleet
		if fleetLabel == "" {
			fleetLabel = "unknown"
		}
		c.devicesSummaryGauge.WithLabelValues(orgIdLabel, fleetLabel, r.Status).Set(float64(r.Count))
	}

	// Update application status metrics
	applicationResults, err := c.store.Device().CountByOrgAndStatus(ctx, nil, store.DeviceStatusTypeApplication, c.cfg.Metrics.DeviceCollector.GroupByFleet)
	if err != nil {
		c.log.WithError(err).Error("Failed to get device application status counts")
		return
	}

	// Update application metrics with actual status values
	for _, r := range applicationResults {
		orgIdLabel := r.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		fleetLabel := r.Fleet
		if fleetLabel == "" {
			fleetLabel = "unknown"
		}
		c.devicesApplicationGauge.WithLabelValues(orgIdLabel, fleetLabel, r.Status).Set(float64(r.Count))
	}

	// Update system update status metrics
	updateResults, err := c.store.Device().CountByOrgAndStatus(ctx, nil, store.DeviceStatusTypeUpdate, c.cfg.Metrics.DeviceCollector.GroupByFleet)
	if err != nil {
		c.log.WithError(err).Error("Failed to get device update status counts")
		return
	}

	// Update update metrics with actual status values
	for _, r := range updateResults {
		orgIdLabel := r.OrgID
		if orgIdLabel == "" {
			orgIdLabel = "unknown"
		}
		fleetLabel := r.Fleet
		if fleetLabel == "" {
			fleetLabel = "unknown"
		}
		c.devicesUpdateGauge.WithLabelValues(orgIdLabel, fleetLabel, r.Status).Set(float64(r.Count))
	}

	c.log.WithFields(logrus.Fields{
		"summary_count":     len(summaryResults),
		"application_count": len(applicationResults),
		"update_count":      len(updateResults),
	}).Debug("Updated device metrics by status type")
}
