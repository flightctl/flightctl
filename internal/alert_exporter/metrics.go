package alert_exporter

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Prometheus metrics for the alert exporter
var (
	// Processing metrics
	ProcessingCyclesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_processing_cycles_total",
		Help: "Total number of processing cycles completed",
	})

	ProcessingDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "flightctl_alert_exporter_processing_duration_seconds",
		Help:    "Time spent processing events in seconds",
		Buckets: prometheus.DefBuckets,
	})

	EventsProcessedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_events_processed_total",
		Help: "Total number of events processed",
	})

	// Alert metrics
	AlertsActiveTotal = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flightctl_alert_exporter_alerts_active_total",
		Help: "Current number of active alerts",
	})

	AlertsCreatedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_alerts_created_total",
		Help: "Total number of alerts created",
	})

	AlertsResolvedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_alerts_resolved_total",
		Help: "Total number of alerts resolved",
	})

	// Alertmanager interaction metrics
	AlertmanagerRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_alertmanager_requests_total",
		Help: "Total number of requests to Alertmanager",
	}, []string{"status"})

	AlertmanagerRequestDurationSeconds = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "flightctl_alert_exporter_alertmanager_request_duration_seconds",
		Help:    "Time spent sending requests to Alertmanager in seconds",
		Buckets: prometheus.DefBuckets,
	})

	AlertmanagerRetriesTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_alertmanager_retries_total",
		Help: "Total number of retries when sending to Alertmanager",
	})

	// Checkpoint metrics
	CheckpointOperationsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_checkpoint_operations_total",
		Help: "Total number of checkpoint operations",
	}, []string{"operation", "status"})

	CheckpointSizeBytes = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flightctl_alert_exporter_checkpoint_size_bytes",
		Help: "Size of the checkpoint data in bytes",
	})

	// Health metrics
	UptimeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flightctl_alert_exporter_uptime_seconds",
		Help: "Time since the alert exporter started in seconds",
	})

	LastSuccessfulProcessingTimestamp = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "flightctl_alert_exporter_last_successful_processing_timestamp",
		Help: "Unix timestamp of the last successful processing cycle",
	})

	ErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "flightctl_alert_exporter_errors_total",
		Help: "Total number of errors encountered",
	}, []string{"component", "type"})
)
