package alert_exporter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type EventProcessor struct {
	log     *logrus.Logger
	handler service.Service
}

func NewEventProcessor(log *logrus.Logger, handler service.Service) *EventProcessor {
	return &EventProcessor{
		log:     log,
		handler: handler,
	}
}

type CheckpointContext struct {
	alerts         map[AlertKey]map[string]*AlertInfo
	alertsCreated  int
	alertsResolved int
}

func (e *EventProcessor) ProcessLatestEvents(ctx context.Context, oldCheckpoint *AlertCheckpoint, metrics *ProcessingMetrics) (*AlertCheckpoint, error) {
	if oldCheckpoint == nil {
		return nil, errors.New("checkpoint cannot be nil")
	}

	logger := e.log.WithFields(logrus.Fields{
		"component":            "event_processor",
		"checkpoint_timestamp": oldCheckpoint.Timestamp,
		"existing_alert_keys":  len(oldCheckpoint.Alerts),
	})

	params := getListEventsParams(oldCheckpoint.Timestamp)
	logger.WithFields(logrus.Fields{
		"newer_than": oldCheckpoint.Timestamp,
		"limit":      *params.Limit,
		"order":      *params.Order,
	}).Debug("Starting event processing")

	checkpointCtx := CheckpointContext{
		alerts:         oldCheckpoint.Alerts,
		alertsCreated:  0,
		alertsResolved: 0,
	}
	if checkpointCtx.alerts == nil {
		checkpointCtx.alerts = make(map[AlertKey]map[string]*AlertInfo)
	}

	totalEvents := 0
	validationErrors := 0
	totalPages := 0

	for {
		totalPages++
		pageLogger := logger.WithField("page_number", totalPages)

		// List the events since the last checkpoint
		events, status := e.handler.ListEvents(ctx, params)
		if status.Code != http.StatusOK {
			pageLogger.WithFields(logrus.Fields{
				"status_code": status.Code,
				"status_msg":  status.Message,
			}).Error("Failed to list events from API")
			return nil, fmt.Errorf("Failed to list events: %s", status.Message)
		}

		pageLogger.WithFields(logrus.Fields{
			"events_in_page": len(events.Items),
			"has_continue":   events.Metadata.Continue != nil,
		}).Debug("Retrieved events page")

		for _, ev := range events.Items {
			totalEvents++
			eventLogger := logger.WithFields(logrus.Fields{
				"event_reason":  ev.Reason,
				"resource_kind": ev.InvolvedObject.Kind,
				"resource_name": ev.InvolvedObject.Name,
				"creation_time": ev.Metadata.CreationTimestamp,
				"event_number":  totalEvents,
			})

			// Skip events without timestamp or with invalid involved object
			if ev.Metadata.CreationTimestamp == nil {
				eventLogger.WithFields(logrus.Fields{
					"resource_name": ev.InvolvedObject.Name,
					"resource_kind": ev.InvolvedObject.Kind,
				}).Warn("Skipping event: no creation timestamp")
				continue
			}

			if strings.TrimSpace(ev.InvolvedObject.Name) == "" {
				eventLogger.WithFields(logrus.Fields{
					"event_type": ev.Type,
					"reason":     ev.Reason,
				}).Warn("Skipping event: no involved object name")
				continue
			}

			eventLogger.Debug("Processing event")
			checkpointCtx.processEvent(ev)
		}

		if events.Metadata.Continue == nil {
			break // No more events to process
		}
		params.Continue = events.Metadata.Continue
	}

	// Fetch the current time from the DB to know where to
	// start from in the next iteration.
	timestamp, status := e.handler.GetDatabaseTime(ctx)
	if status.Code != http.StatusOK {
		logger.WithFields(logrus.Fields{
			"status_code": status.Code,
			"status_msg":  status.Message,
		}).Error("Failed to get database time")
		return nil, fmt.Errorf("failed to get DB time: %s", status.Message)
	}

	newCheckpoint := &AlertCheckpoint{
		Version:   CurrentAlertCheckpointVersion,
		Alerts:    checkpointCtx.alerts,
		Timestamp: timestamp.Format(time.RFC3339Nano),
	}

	logger.WithFields(logrus.Fields{
		"total_events":      totalEvents,
		"validation_errors": validationErrors,
		"pages_processed":   totalPages,
		"new_timestamp":     newCheckpoint.Timestamp,
		"final_alert_keys":  len(newCheckpoint.Alerts),
		"total_alert_count": e.countTotalAlerts(newCheckpoint.Alerts),
	}).Info("Event processing completed")

	if validationErrors > 0 {
		logger.WithField("validation_errors", validationErrors).
			Warn("Some events were skipped due to validation errors")
	}

	// Update metrics with results
	metrics.AlertsCreated = checkpointCtx.alertsCreated
	metrics.AlertsResolved = checkpointCtx.alertsResolved
	metrics.EventsProcessed = totalEvents

	return newCheckpoint, nil
}

// countTotalAlerts counts the total number of alerts across all resources
func (e *EventProcessor) countTotalAlerts(alerts map[AlertKey]map[string]*AlertInfo) int {
	total := 0
	for _, reasons := range alerts {
		total += len(reasons)
	}
	return total
}

func getListEventsParams(newerThan string) api.ListEventsParams {
	eventsOfInterest := []api.EventReason{
		api.EventReasonDeviceApplicationDegraded,
		api.EventReasonDeviceApplicationError,
		api.EventReasonDeviceApplicationHealthy,
		api.EventReasonDeviceCPUCritical,
		api.EventReasonDeviceCPUNormal,
		api.EventReasonDeviceCPUWarning,
		api.EventReasonDeviceConnected,
		api.EventReasonDeviceDisconnected,
		api.EventReasonDeviceMemoryCritical,
		api.EventReasonDeviceMemoryNormal,
		api.EventReasonDeviceMemoryWarning,
		api.EventReasonDeviceDiskCritical,
		api.EventReasonDeviceDiskNormal,
		api.EventReasonDeviceDiskWarning,
		api.EventReasonResourceDeleted,
		api.EventReasonDeviceDecommissioned,
	}

	fieldSelectors := []string{
		fmt.Sprintf("reason in (%s)",
			strings.Join(lo.Map(eventsOfInterest, func(r api.EventReason, _ int) string {
				return string(r)
			}), ",")),
	}
	if newerThan != "" {
		fieldSelectors = append(fieldSelectors,
			fmt.Sprintf("metadata.creationTimestamp>=%s", newerThan))
	}

	return api.ListEventsParams{
		Order:         lo.ToPtr(api.Asc), // Oldest to newest
		FieldSelector: lo.ToPtr(strings.Join(fieldSelectors, ",")),
		Limit:         lo.ToPtr(int32(1000)),
	}
}

var (
	appStatusGroup = []string{string(api.EventReasonDeviceApplicationError), string(api.EventReasonDeviceApplicationDegraded)}
	cpuGroup       = []string{string(api.EventReasonDeviceCPUCritical), string(api.EventReasonDeviceCPUWarning)}
	memoryGroup    = []string{string(api.EventReasonDeviceMemoryCritical), string(api.EventReasonDeviceMemoryWarning)}
	diskGroup      = []string{string(api.EventReasonDeviceDiskCritical), string(api.EventReasonDeviceDiskWarning)}
)

func (c *CheckpointContext) processEvent(event api.Event) {
	switch event.Reason {
	case api.EventReasonResourceDeleted, api.EventReasonDeviceDecommissioned:
		c.resolveAllAlertsForResource(event)
	// Applications
	case api.EventReasonDeviceApplicationError:
		c.setAlert(event, string(api.EventReasonDeviceApplicationError), appStatusGroup)
	case api.EventReasonDeviceApplicationDegraded:
		c.setAlert(event, string(api.EventReasonDeviceApplicationDegraded), appStatusGroup)
	case api.EventReasonDeviceApplicationHealthy:
		c.clearAlertGroup(event, appStatusGroup)
	// CPU
	case api.EventReasonDeviceCPUCritical:
		c.setAlert(event, string(api.EventReasonDeviceCPUCritical), cpuGroup)
	case api.EventReasonDeviceCPUWarning:
		c.setAlert(event, string(api.EventReasonDeviceCPUWarning), cpuGroup)
	case api.EventReasonDeviceCPUNormal:
		c.clearAlertGroup(event, cpuGroup)
	// Memory
	case api.EventReasonDeviceMemoryCritical:
		c.setAlert(event, string(api.EventReasonDeviceMemoryCritical), memoryGroup)
	case api.EventReasonDeviceMemoryWarning:
		c.setAlert(event, string(api.EventReasonDeviceMemoryWarning), memoryGroup)
	case api.EventReasonDeviceMemoryNormal:
		c.clearAlertGroup(event, memoryGroup)
	// Disk
	case api.EventReasonDeviceDiskCritical:
		c.setAlert(event, string(api.EventReasonDeviceDiskCritical), diskGroup)
	case api.EventReasonDeviceDiskWarning:
		c.setAlert(event, string(api.EventReasonDeviceDiskWarning), diskGroup)
	case api.EventReasonDeviceDiskNormal:
		c.clearAlertGroup(event, diskGroup)
	// Device connection status
	case api.EventReasonDeviceDisconnected:
		c.setAlert(event, string(api.EventReasonDeviceDisconnected), nil)
	case api.EventReasonDeviceConnected:
		c.clearAlertGroup(event, []string{string(api.EventReasonDeviceDisconnected)})
	}
}

func AlertKeyFromEvent(event api.Event) AlertKey {
	return NewAlertKey(store.NullOrgId.String(), event.InvolvedObject.Kind, event.InvolvedObject.Name)
}

func (c *CheckpointContext) resolveAllAlertsForResource(event api.Event) {
	k := AlertKeyFromEvent(event)
	if _, exists := c.alerts[k]; !exists {
		return
	}
	for _, v := range c.alerts[k] {
		if v.EndsAt == nil {
			v.EndsAt = event.Metadata.CreationTimestamp
			c.alertsResolved++
		}
	}
}

func (c *CheckpointContext) setAlert(event api.Event, reason string, group []string) {
	// Clear other alerts in the same group
	k := AlertKeyFromEvent(event)
	if _, exists := c.alerts[k]; !exists {
		c.alerts[k] = map[string]*AlertInfo{}
	}
	for _, r := range group {
		if _, exists := c.alerts[k][r]; exists {
			if reason != r && c.alerts[k][r].EndsAt == nil {
				c.alerts[k][r].EndsAt = event.Metadata.CreationTimestamp
				c.alertsResolved++
			}
		}
	}

	// Set the alert if not already set
	alertExists := false
	if _, exists := c.alerts[k][reason]; !exists {
		c.alerts[k][reason] = &AlertInfo{}
	} else {
		alertExists = c.alerts[k][reason].EndsAt == nil
	}

	if !c.alerts[k][reason].StartsAt.Equal(*event.Metadata.CreationTimestamp) {
		c.alerts[k][reason].ResourceName = event.InvolvedObject.Name
		c.alerts[k][reason].ResourceKind = event.InvolvedObject.Kind
		c.alerts[k][reason].OrgID = store.NullOrgId.String()
		c.alerts[k][reason].Reason = reason
		c.alerts[k][reason].StartsAt = *event.Metadata.CreationTimestamp
		c.alerts[k][reason].EndsAt = nil

		// Track if this is a new alert (not already active)
		if !alertExists {
			c.alertsCreated++
		}
	}
}

func (c *CheckpointContext) clearAlertGroup(event api.Event, group []string) {
	k := AlertKeyFromEvent(event)
	if _, exists := c.alerts[k]; !exists {
		// No alerts for this resource
		return
	}

	// Clear all alerts in the group
	for _, r := range group {
		if _, exists := c.alerts[k][r]; exists {
			if c.alerts[k][r].EndsAt == nil {
				c.alerts[k][r].EndsAt = event.Metadata.CreationTimestamp
				c.alertsResolved++
			}
		}
	}
}
