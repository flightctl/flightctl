package shutdown

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// ShutdownCollector implements NamedCollector and gathers shutdown-related metrics.
type ShutdownCollector struct {
	// Shutdown state metrics
	shutdownStateGauge       *prometheus.GaugeVec
	shutdownInitiatedCounter prometheus.Counter
	shutdownCompletedCounter *prometheus.CounterVec

	// Component shutdown metrics
	componentShutdownDuration *prometheus.HistogramVec
	componentShutdownStatus   *prometheus.CounterVec
	componentTimeoutCounter   prometheus.Counter
	componentsActiveGauge     prometheus.Gauge

	// Error and failure metrics
	shutdownErrorsCounter  *prometheus.CounterVec
	failFastEventsCounter  *prometheus.CounterVec
	resourceCleanupCounter *prometheus.CounterVec

	// Timing metrics
	totalShutdownDuration prometheus.Histogram
	lastShutdownTimestamp prometheus.Gauge
	gracefulShutdownGauge prometheus.Gauge

	// Signal handling metrics
	signalReceivedCounter *prometheus.CounterVec
	signalTimeoutCounter  prometheus.Counter

	log logrus.FieldLogger
	ctx context.Context
	mu  sync.RWMutex

	// State tracking for metrics
	shutdownStartTime  time.Time
	activeComponents   map[string]bool
	shutdownInProgress bool
}

// NewShutdownCollector creates a ShutdownCollector.
func NewShutdownCollector(ctx context.Context, log logrus.FieldLogger) *ShutdownCollector {
	collector := &ShutdownCollector{
		// Shutdown state metrics
		shutdownStateGauge: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "flightctl_shutdown_state",
			Help: "Current shutdown state (0=idle, 1=initiated, 2=in_progress, 3=completed, 4=failed)",
		}, []string{"service"}),
		shutdownInitiatedCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_shutdown_initiated_total",
			Help: "Total number of shutdown sequences initiated",
		}),
		shutdownCompletedCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_completed_total",
			Help: "Total number of shutdown sequences completed by outcome",
		}, []string{"outcome"}), // success, timeout, error, cancelled

		// Component shutdown metrics
		componentShutdownDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "flightctl_shutdown_component_duration_seconds",
			Help:    "Histogram of component shutdown duration by priority and component",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 20), // 1ms to ~524s
		}, []string{"component", "priority"}),
		componentShutdownStatus: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_component_status_total",
			Help: "Component shutdown outcomes by component and status",
		}, []string{"component", "priority", "status"}), // success, timeout, error
		componentTimeoutCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_shutdown_component_timeouts_total",
			Help: "Total number of component shutdown timeouts",
		}),
		componentsActiveGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_shutdown_components_active",
			Help: "Number of components currently being shut down",
		}),

		// Error and failure metrics
		shutdownErrorsCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_errors_total",
			Help: "Total shutdown errors by type and component",
		}, []string{"component", "error_type"}),
		failFastEventsCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_fail_fast_events_total",
			Help: "Total fail-fast events by component and reason",
		}, []string{"component", "reason"}),
		resourceCleanupCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_resource_cleanup_total",
			Help: "Resource cleanup events by resource type and status",
		}, []string{"resource_type", "status"}), // database, kvstore, queue - success, failed

		// Timing metrics
		totalShutdownDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_shutdown_total_duration_seconds",
			Help:    "Histogram of total shutdown duration from initiation to completion",
			Buckets: prometheus.ExponentialBuckets(0.1, 2, 15), // 100ms to ~1638s
		}),
		lastShutdownTimestamp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_shutdown_last_timestamp_seconds",
			Help: "Unix timestamp of last shutdown event (initiation)",
		}),
		gracefulShutdownGauge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_shutdown_graceful_enabled",
			Help: "Whether graceful shutdown is enabled (1) or disabled (0)",
		}),

		// Signal handling metrics
		signalReceivedCounter: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "flightctl_shutdown_signals_received_total",
			Help: "Total shutdown signals received by signal type",
		}, []string{"signal"}), // SIGTERM, SIGINT, SIGQUIT
		signalTimeoutCounter: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_shutdown_signal_timeouts_total",
			Help: "Total signal handling timeouts (forced shutdown)",
		}),

		log:                log,
		ctx:                ctx,
		activeComponents:   make(map[string]bool),
		shutdownInProgress: false,
	}

	// Set initial state
	collector.gracefulShutdownGauge.Set(1) // Assume graceful shutdown is enabled
	collector.log.Debug("Shutdown metrics collector initialized")

	return collector
}

func (c *ShutdownCollector) MetricsName() string {
	return "shutdown"
}

func (c *ShutdownCollector) Describe(ch chan<- *prometheus.Desc) {
	c.shutdownStateGauge.Describe(ch)
	c.shutdownInitiatedCounter.Describe(ch)
	c.shutdownCompletedCounter.Describe(ch)
	c.componentShutdownDuration.Describe(ch)
	c.componentShutdownStatus.Describe(ch)
	c.componentTimeoutCounter.Describe(ch)
	c.componentsActiveGauge.Describe(ch)
	c.shutdownErrorsCounter.Describe(ch)
	c.failFastEventsCounter.Describe(ch)
	c.resourceCleanupCounter.Describe(ch)
	c.totalShutdownDuration.Describe(ch)
	c.lastShutdownTimestamp.Describe(ch)
	c.gracefulShutdownGauge.Describe(ch)
	c.signalReceivedCounter.Describe(ch)
	c.signalTimeoutCounter.Describe(ch)
}

func (c *ShutdownCollector) Collect(ch chan<- prometheus.Metric) {
	c.shutdownStateGauge.Collect(ch)
	c.shutdownInitiatedCounter.Collect(ch)
	c.shutdownCompletedCounter.Collect(ch)
	c.componentShutdownDuration.Collect(ch)
	c.componentShutdownStatus.Collect(ch)
	c.componentTimeoutCounter.Collect(ch)
	c.componentsActiveGauge.Collect(ch)
	c.shutdownErrorsCounter.Collect(ch)
	c.failFastEventsCounter.Collect(ch)
	c.resourceCleanupCounter.Collect(ch)
	c.totalShutdownDuration.Collect(ch)
	c.lastShutdownTimestamp.Collect(ch)
	c.gracefulShutdownGauge.Collect(ch)
	c.signalReceivedCounter.Collect(ch)
	c.signalTimeoutCounter.Collect(ch)
}

// Metric update methods to be called by shutdown management code

func (c *ShutdownCollector) RecordShutdownInitiated(serviceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.shutdownInitiatedCounter.Inc()
	c.shutdownStateGauge.WithLabelValues(serviceName).Set(1) // initiated
	c.lastShutdownTimestamp.Set(float64(time.Now().Unix()))
	c.shutdownStartTime = time.Now()
	c.shutdownInProgress = true

	c.log.WithField("service", serviceName).Debug("Recorded shutdown initiated")
}

func (c *ShutdownCollector) RecordShutdownInProgress(serviceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.shutdownStateGauge.WithLabelValues(serviceName).Set(2) // in_progress
	c.log.WithField("service", serviceName).Debug("Recorded shutdown in progress")
}

func (c *ShutdownCollector) RecordShutdownCompleted(serviceName string, outcome string, totalDuration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.shutdownCompletedCounter.WithLabelValues(outcome).Inc()

	var stateValue float64
	switch outcome {
	case "success":
		stateValue = 3 // completed successfully
	case "error", "timeout", "cancelled":
		stateValue = 4 // failed
	default:
		stateValue = 4 // unknown, treat as failed
	}

	c.shutdownStateGauge.WithLabelValues(serviceName).Set(stateValue)
	c.totalShutdownDuration.Observe(totalDuration.Seconds())
	c.shutdownInProgress = false

	c.log.WithFields(logrus.Fields{
		"service":  serviceName,
		"outcome":  outcome,
		"duration": totalDuration,
	}).Debug("Recorded shutdown completed")
}

func (c *ShutdownCollector) RecordComponentShutdownStart(component string, priority int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.activeComponents[component] = true
	c.componentsActiveGauge.Set(float64(len(c.activeComponents)))

	c.log.WithFields(logrus.Fields{
		"component": component,
		"priority":  priority,
	}).Debug("Recorded component shutdown start")
}

func (c *ShutdownCollector) RecordComponentShutdownEnd(component string, priority int, status string, duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.activeComponents, component)
	c.componentsActiveGauge.Set(float64(len(c.activeComponents)))

	priorityStr := fmt.Sprintf("%d", priority)
	c.componentShutdownDuration.WithLabelValues(component, priorityStr).Observe(duration.Seconds())
	c.componentShutdownStatus.WithLabelValues(component, priorityStr, status).Inc()

	if status == "timeout" {
		c.componentTimeoutCounter.Inc()
	}

	c.log.WithFields(logrus.Fields{
		"component": component,
		"priority":  priority,
		"status":    status,
		"duration":  duration,
	}).Debug("Recorded component shutdown end")
}

func (c *ShutdownCollector) RecordShutdownError(component string, errorType string) {
	c.shutdownErrorsCounter.WithLabelValues(component, errorType).Inc()
	c.log.WithFields(logrus.Fields{
		"component":  component,
		"error_type": errorType,
	}).Debug("Recorded shutdown error")
}

func (c *ShutdownCollector) RecordFailFastEvent(component string, reason string) {
	c.failFastEventsCounter.WithLabelValues(component, reason).Inc()
	c.log.WithFields(logrus.Fields{
		"component": component,
		"reason":    reason,
	}).Info("Recorded fail-fast event")
}

func (c *ShutdownCollector) RecordResourceCleanup(resourceType string, status string) {
	c.resourceCleanupCounter.WithLabelValues(resourceType, status).Inc()
	c.log.WithFields(logrus.Fields{
		"resource_type": resourceType,
		"status":        status,
	}).Debug("Recorded resource cleanup")
}

func (c *ShutdownCollector) RecordSignalReceived(signal string) {
	c.signalReceivedCounter.WithLabelValues(signal).Inc()
	c.log.WithField("signal", signal).Info("Recorded shutdown signal received")
}

func (c *ShutdownCollector) RecordSignalTimeout() {
	c.signalTimeoutCounter.Inc()
	c.log.Warn("Recorded signal handling timeout")
}

func (c *ShutdownCollector) IsShutdownInProgress() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shutdownInProgress
}
