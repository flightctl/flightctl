// Package alert_exporter monitors recent events and maintains a live view of active alerts.
//
// Alert Structure:
//   Alerts are stored in-memory using the following structure:
//
//     map[AlertKey]map[string]*AlertInfo
//
//   - AlertKey is a composite string in the format "org:kind:name", uniquely identifying a resource.
//   - The nested map tracks active alert reasons (as strings) for that resource.
//
// Alert Logic:
//   - Certain alert reasons are mutually exclusive and grouped (e.g., CPU status, application health).
//     Only one alert from a group may be active for a resource at a time.
//   - When a new event is processed:
//     - If it's part of an exclusive group (e.g., DeviceApplicationError), other group members are removed.
//     - If it's a "normal" or healthy event (e.g., DeviceCPUNormal), the entire group is cleared.
//     - DeviceDisconnected is added to the alert set, and DeviceConnected removes it.
//     - Terminal events (e.g., ResourceDeleted, DeviceDecommissioned) remove all alerts for the resource.
//
// Alertmanager Integration:
//   - Changes to alert states are pushed to Alertmanager via HTTP POST requests in batches.
//   - Alerts have labels:
//     - alertname: the reason for the alert
//     - resource: the name of the resource
//     - org_id: the organization ID of the resource
//   - Sets the startsAt field when an alert is triggered, and endsAt upon resolution.
//
// Checkpointing:
//   - The exporter periodically saves its state (active alerts and timestamp).
//   - On startup, it resumes from the last checkpoint to avoid reprocessing old events.

package alert_exporter

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

const CurrentAlertCheckpointVersion = 1

type AlertKey string

type AlertInfo struct {
	ResourceName string
	ResourceKind string
	OrgID        string
	Reason       string
	StartsAt     time.Time
	EndsAt       *time.Time
}

type AlertCheckpoint struct {
	Version   int
	Timestamp string
	Alerts    map[AlertKey]map[string]*AlertInfo
}

// ProcessingMetrics tracks operational metrics for monitoring and observability
type ProcessingMetrics struct {
	CycleStartTime   time.Time
	EventsProcessed  int
	AlertsCreated    int
	AlertsResolved   int
	ProcessingTimeMs int64
	SendingTimeMs    int64
	CheckpointTimeMs int64
	TotalCycleTimeMs int64
	ActiveAlerts     int
}

func NewAlertKey(org string, kind string, name string) AlertKey {
	return AlertKey(fmt.Sprintf("%s:%s:%s", org, kind, name))
}

type AlertExporter struct {
	log     *logrus.Logger
	handler service.Service
	config  *config.Config
}

func NewAlertExporter(log *logrus.Logger, handler service.Service, config *config.Config) *AlertExporter {
	return &AlertExporter{
		log:     log,
		handler: handler,
		config:  config,
	}
}

func (a *AlertExporter) Poll(ctx context.Context) error {
	// Record processing cycle start
	startTime := time.Now()
	defer func() {
		ProcessingCyclesTotal.Inc()
		ProcessingDurationSeconds.Observe(time.Since(startTime).Seconds())
	}()

	a.log.WithFields(logrus.Fields{
		"component":        "alert_exporter",
		"polling_interval": a.config.Service.AlertPollingInterval,
		"alertmanager":     fmt.Sprintf("%s:%d", a.config.Alertmanager.Hostname, a.config.Alertmanager.Port),
	}).Info("Starting alert exporter polling")

	checkpointManager := NewCheckpointManager(a.log, a.handler)
	eventProcessor := NewEventProcessor(a.log, a.handler)
	alertSender := NewAlertSender(a.log, a.config.Alertmanager.Hostname, a.config.Alertmanager.Port, a.config)

	ticker := time.NewTicker(time.Duration(a.config.Service.AlertPollingInterval))
	defer ticker.Stop()

	checkpoint := checkpointManager.LoadCheckpoint(ctx)

	cycleCount := 0

	for {
		select {
		case <-ctx.Done():
			a.log.WithFields(logrus.Fields{
				"component":   "alert_exporter",
				"cycles_run":  cycleCount,
				"context_err": ctx.Err(),
			}).Info("Alert exporter stopping due to context cancellation")
			return ctx.Err()
		case <-ticker.C:
		}

		cycleCount++
		a.processingCycle(ctx, checkpointManager, eventProcessor, alertSender, &checkpoint, cycleCount)
	}
}

func (a *AlertExporter) processingCycle(
	ctx context.Context,
	checkpointManager *CheckpointManager,
	eventProcessor *EventProcessor,
	alertSender *AlertSender,
	checkpoint **AlertCheckpoint,
	cycleNumber int,
) {
	// Start OpenTelemetry span for the processing cycle
	ctx, span := tracing.StartSpan(ctx, "flightctl/alert-exporter", "ProcessingCycle")
	defer span.End()

	// Add cycle information as span attributes
	span.SetAttributes(
		attribute.Int("cycle_number", cycleNumber),
	)

	metrics := &ProcessingMetrics{
		CycleStartTime: time.Now(),
	}

	logger := a.log.WithFields(logrus.Fields{
		"component":    "alert_exporter",
		"cycle_number": cycleNumber,
		"trace_id":     span.SpanContext().TraceID().String(),
		"span_id":      span.SpanContext().SpanID().String(),
	})

	logger.Debug("Starting processing cycle")

	tickerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Process events with span
	processStart := time.Now()
	processCtx, processSpan := tracing.StartSpan(tickerCtx, "flightctl/alert-exporter", "ProcessEvents")
	newCheckpoint, err := eventProcessor.ProcessLatestEvents(processCtx, *checkpoint, metrics)
	processSpan.End()
	if err != nil {
		span.RecordError(err)
		logger.WithFields(logrus.Fields{
			"error":           err,
			"processing_time": time.Since(processStart),
		}).Error("Failed processing events")
		return
	}
	metrics.ProcessingTimeMs = time.Since(processStart).Milliseconds()

	// Send alerts with span
	sendStart := time.Now()
	_, sendSpan := tracing.StartSpan(tickerCtx, "flightctl/alert-exporter", "SendAlerts")
	err = alertSender.SendAlerts(newCheckpoint)
	sendSpan.End()
	if err != nil {
		span.RecordError(err)
		logger.WithFields(logrus.Fields{
			"error":        err,
			"sending_time": time.Since(sendStart),
		}).Error("Failed sending alerts")
		return
	}
	metrics.SendingTimeMs = time.Since(sendStart).Milliseconds()

	// Store checkpoint with span
	checkpointStart := time.Now()
	ckptCtx, ckptSpan := tracing.StartSpan(tickerCtx, "flightctl/alert-exporter", "StoreCheckpoint")
	err = checkpointManager.StoreCheckpoint(ckptCtx, newCheckpoint)
	ckptSpan.End()
	if err != nil {
		span.RecordError(err)
		logger.WithFields(logrus.Fields{
			"error":           err,
			"checkpoint_time": time.Since(checkpointStart),
		}).Error("Failed storing checkpoint")
		return
	}
	metrics.CheckpointTimeMs = time.Since(checkpointStart).Milliseconds()

	// Calculate active alerts count - only after successful send and store
	a.calculateActiveAlerts(metrics, newCheckpoint, logger)
	metrics.TotalCycleTimeMs = time.Since(metrics.CycleStartTime).Milliseconds()

	*checkpoint = newCheckpoint

	// Add metrics as span attributes
	span.SetAttributes(
		attribute.Int("events_processed", metrics.EventsProcessed),
		attribute.Int("alerts_created", metrics.AlertsCreated),
		attribute.Int("alerts_resolved", metrics.AlertsResolved),
		attribute.Int64("processing_time_ms", metrics.ProcessingTimeMs),
		attribute.Int64("sending_time_ms", metrics.SendingTimeMs),
		attribute.Int64("checkpoint_time_ms", metrics.CheckpointTimeMs),
		attribute.Int64("total_cycle_time_ms", metrics.TotalCycleTimeMs),
	)

	// Log successful completion with tracing context
	logger.WithFields(logrus.Fields{
		"events_processed":    metrics.EventsProcessed,
		"alerts_created":      metrics.AlertsCreated,
		"alerts_resolved":     metrics.AlertsResolved,
		"processing_time_ms":  metrics.ProcessingTimeMs,
		"sending_time_ms":     metrics.SendingTimeMs,
		"checkpoint_time_ms":  metrics.CheckpointTimeMs,
		"total_cycle_time_ms": metrics.TotalCycleTimeMs,
		"active_alert_keys":   len(newCheckpoint.Alerts),
	}).Info("Processing cycle completed successfully")

	// Log performance warnings if needed
	if metrics.TotalCycleTimeMs > 5000 { // More than 5 seconds
		logger.WithFields(logrus.Fields{
			"total_cycle_time_ms": metrics.TotalCycleTimeMs,
			"processing_time_ms":  metrics.ProcessingTimeMs,
			"sending_time_ms":     metrics.SendingTimeMs,
			"checkpoint_time_ms":  metrics.CheckpointTimeMs,
		}).Warn("Processing cycle took longer than expected")
	}
}

// calculateActiveAlerts counts and updates the active alerts metric
func (a *AlertExporter) calculateActiveAlerts(metrics *ProcessingMetrics, newCheckpoint *AlertCheckpoint, logger *logrus.Entry) {
	// Count total active alerts
	activeAlerts := 0
	for _, reasons := range newCheckpoint.Alerts {
		activeAlerts += len(reasons)
	}

	metrics.ActiveAlerts = activeAlerts

	// Update Prometheus metrics
	if metrics.AlertsCreated > 0 {
		AlertsCreatedTotal.Add(float64(metrics.AlertsCreated))
	}
	if metrics.AlertsResolved > 0 {
		AlertsResolvedTotal.Add(float64(metrics.AlertsResolved))
	}
	AlertsActiveTotal.Set(float64(metrics.ActiveAlerts))
	EventsProcessedTotal.Add(float64(metrics.EventsProcessed))

	// Record successful processing timestamp
	LastSuccessfulProcessingTimestamp.SetToCurrentTime()

	logger.WithFields(logrus.Fields{
		"events_processed": metrics.EventsProcessed,
		"alerts_created":   metrics.AlertsCreated,
		"alerts_resolved":  metrics.AlertsResolved,
		"active_alerts":    metrics.ActiveAlerts,
		"processing_time":  metrics.ProcessingTimeMs,
	}).Info("Processing cycle completed successfully")
}
