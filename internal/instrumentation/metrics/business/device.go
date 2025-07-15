package business

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// DeviceCollector implements NamedCollector and gathers device-related business metrics.
type DeviceCollector struct {
	// Summary status metrics
	devicesSummaryGauge *prometheus.GaugeVec

	// Application status metrics
	devicesApplicationGauge *prometheus.GaugeVec

	// System update status metrics
	devicesUpdateGauge *prometheus.GaugeVec

	// Device lifecycle metrics
	deviceEnrollmentsCounter      *prometheus.CounterVec
	deviceDecommissioningsCounter *prometheus.CounterVec
	deviceHeartbeatsCounter       *prometheus.CounterVec
	deviceConfigurationDriftGauge *prometheus.GaugeVec
	deviceUpdateSuccessCounter    *prometheus.CounterVec
	deviceUpdateFailureCounter    *prometheus.CounterVec
	deviceUpdateDurationHistogram *prometheus.HistogramVec

	store store.Store
	log   logrus.FieldLogger
	mu    sync.RWMutex
	ctx   context.Context
}

func NewDeviceCollector(ctx context.Context, store store.Store, log logrus.FieldLogger) *DeviceCollector {
	collector := &DeviceCollector{
		// Summary status metrics
		devicesSummaryGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_summary_total",
			Help: "Total number of devices managed (by summary status)",
		}, []string{"organization_id", "version", "fleet", "status"}),

		// Application status metrics
		devicesApplicationGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_application_total",
			Help: "Total number of devices managed (by application status)",
		}, []string{"organization_id", "version", "fleet", "status"}),

		// System update status metrics
		devicesUpdateGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_devices_update_total",
			Help: "Total number of devices managed (by system update status)",
		}, []string{"organization_id", "version", "fleet", "status"}),

		// Device lifecycle metrics
		deviceEnrollmentsCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_device_enrollments_total",
			Help: "Count of new device enrollments",
		}, []string{"organization_id", "fleet"}),

		deviceDecommissioningsCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_device_decommissionings_total",
			Help: "Count of device decommissionings",
		}, []string{"organization_id", "fleet"}),

		deviceHeartbeatsCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_device_heartbeats_total",
			Help: "Rate of heartbeats received from agents",
		}, []string{"organization_id", "fleet"}),

		deviceConfigurationDriftGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_device_configuration_drift_total",
			Help: "Count of devices with configuration drift (actual vs. desired state)",
		}, []string{"organization_id", "fleet", "drift_type"}),

		deviceUpdateSuccessCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_device_update_success_total",
			Help: "Count of successful device updates/rollouts",
		}, []string{"organization_id", "fleet", "update_type"}),

		deviceUpdateFailureCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_device_update_failure_total",
			Help: "Count of failed device updates/rollouts",
		}, []string{"organization_id", "fleet", "update_type", "failure_reason"}),

		deviceUpdateDurationHistogram: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "flightctl_device_update_duration_seconds",
			Help:    "Time taken for device updates to complete",
			Buckets: prometheus.DefBuckets,
		}, []string{"organization_id", "fleet", "update_type"}),

		store: store,
		log:   log,
		ctx:   ctx,
	}

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
	c.deviceEnrollmentsCounter.Describe(ch)
	c.deviceDecommissioningsCounter.Describe(ch)
	c.deviceHeartbeatsCounter.Describe(ch)
	c.deviceConfigurationDriftGauge.Describe(ch)
	c.deviceUpdateSuccessCounter.Describe(ch)
	c.deviceUpdateFailureCounter.Describe(ch)
	c.deviceUpdateDurationHistogram.Describe(ch)
}

func (c *DeviceCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	c.devicesSummaryGauge.Collect(ch)
	c.devicesApplicationGauge.Collect(ch)
	c.devicesUpdateGauge.Collect(ch)
	c.deviceEnrollmentsCounter.Collect(ch)
	c.deviceDecommissioningsCounter.Collect(ch)
	c.deviceHeartbeatsCounter.Collect(ch)
	c.deviceConfigurationDriftGauge.Collect(ch)
	c.deviceUpdateSuccessCounter.Collect(ch)
	c.deviceUpdateFailureCounter.Collect(ch)
	c.deviceUpdateDurationHistogram.Collect(ch)
}

func (c *DeviceCollector) sampleDeviceMetrics() {
	ticker := time.NewTicker(30 * time.Second) // Sample every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
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
	c.deviceConfigurationDriftGauge.Reset()

	// Update summary status metrics
	summaryResults, err := c.store.Device().CountByFleetAndStatus(ctx, nil, nil, store.DeviceStatusTypeSummary)
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
		c.devicesSummaryGauge.WithLabelValues(orgIdLabel, r.Version, r.Fleet, r.Status).Set(float64(r.Count))
	}

	// Update application status metrics
	applicationResults, err := c.store.Device().CountByFleetAndStatus(ctx, nil, nil, store.DeviceStatusTypeApplication)
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
		c.devicesApplicationGauge.WithLabelValues(orgIdLabel, r.Version, r.Fleet, r.Status).Set(float64(r.Count))
	}

	// Update system update status metrics
	updateResults, err := c.store.Device().CountByFleetAndStatus(ctx, nil, nil, store.DeviceStatusTypeUpdate)
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
		c.devicesUpdateGauge.WithLabelValues(orgIdLabel, r.Version, r.Fleet, r.Status).Set(float64(r.Count))
	}

	c.log.WithFields(logrus.Fields{
		"summary_count":     len(summaryResults),
		"application_count": len(applicationResults),
		"update_count":      len(updateResults),
	}).Debug("Updated device metrics by status type")
}

// IncrementDeviceEnrollment increments the device enrollment counter
func (c *DeviceCollector) IncrementDeviceEnrollment(orgID, fleet string) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	c.deviceEnrollmentsCounter.WithLabelValues(orgID, fleet).Inc()
}

// IncrementDeviceDecommissioning increments the device decommissioning counter
func (c *DeviceCollector) IncrementDeviceDecommissioning(orgID, fleet string) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	c.deviceDecommissioningsCounter.WithLabelValues(orgID, fleet).Inc()
}

// IncrementDeviceHeartbeat increments the device heartbeat counter
func (c *DeviceCollector) IncrementDeviceHeartbeat(orgID, fleet string) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	c.deviceHeartbeatsCounter.WithLabelValues(orgID, fleet).Inc()
}

// SetDeviceConfigurationDrift sets the configuration drift gauge for a specific drift type
func (c *DeviceCollector) SetDeviceConfigurationDrift(orgID, fleet, driftType string, count int) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	if driftType == "" {
		driftType = "unknown"
	}
	c.deviceConfigurationDriftGauge.WithLabelValues(orgID, fleet, driftType).Set(float64(count))
}

// IncrementDeviceUpdateSuccess increments the successful device update counter
func (c *DeviceCollector) IncrementDeviceUpdateSuccess(orgID, fleet, updateType string) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	if updateType == "" {
		updateType = "unknown"
	}
	c.deviceUpdateSuccessCounter.WithLabelValues(orgID, fleet, updateType).Inc()
}

// IncrementDeviceUpdateFailure increments the failed device update counter
func (c *DeviceCollector) IncrementDeviceUpdateFailure(orgID, fleet, updateType, failureReason string) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	if updateType == "" {
		updateType = "unknown"
	}
	if failureReason == "" {
		failureReason = "unknown"
	}
	c.deviceUpdateFailureCounter.WithLabelValues(orgID, fleet, updateType, failureReason).Inc()
}

// ObserveDeviceUpdateDuration records the duration of a device update
func (c *DeviceCollector) ObserveDeviceUpdateDuration(orgID, fleet, updateType string, duration time.Duration) {
	if orgID == "" {
		orgID = "unknown"
	}
	if fleet == "" {
		fleet = "unknown"
	}
	if updateType == "" {
		updateType = "unknown"
	}
	c.deviceUpdateDurationHistogram.WithLabelValues(orgID, fleet, updateType).Observe(duration.Seconds())
}
