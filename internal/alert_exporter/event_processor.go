package alert_exporter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/google/uuid"
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

	// Get all organizations
	orgs, status := e.handler.ListOrganizations(ctx, api.ListOrganizationsParams{})
	if status.Code != http.StatusOK {
		logger.WithFields(logrus.Fields{
			"status_code": status.Code,
			"status_msg":  status.Message,
		}).Error("Failed to list organizations")
		return nil, fmt.Errorf("failed to list organizations: %s", status.Message)
	}

	logger.WithField("org_count", len(orgs.Items)).Info("Processing events for organizations")

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

	// Process events for each organization
	for _, org := range orgs.Items {
		orgID, err := uuid.Parse(lo.FromPtr(org.Metadata.Name))
		if err != nil {
			logger.WithFields(logrus.Fields{
				"org_id": lo.FromPtr(org.Metadata.Name),
				"error":  err,
			}).Error("Failed to parse organization ID")
			validationErrors++
			continue
		}

		orgLogger := logger.WithFields(logrus.Fields{
			"org_id":           orgID,
			"org_display_name": lo.FromPtrOr(org.Spec.DisplayName, ""),
		})

		orgLogger.Debug("Processing events for organization")

		events, pages, orgValidationErrors, err := e.processOrganizationEvents(ctx, orgID, oldCheckpoint.Timestamp, &checkpointCtx, orgLogger)
		if err != nil {
			orgLogger.WithError(err).Error("Failed to process events for organization")
			continue // Continue processing other orgs even if one fails
		}

		totalEvents += events
		totalPages += pages
		validationErrors += orgValidationErrors

		orgLogger.WithFields(logrus.Fields{
			"events_processed": events,
			"pages_processed":  pages,
		}).Debug("Completed processing events for organization")
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
		"orgs_processed":    len(orgs.Items),
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

// processOrganizationEvents processes events for a specific organization
func (e *EventProcessor) processOrganizationEvents(ctx context.Context, orgID uuid.UUID, timestamp string, checkpointCtx *CheckpointContext, logger *logrus.Entry) (int, int, int, error) {
	params := getListEventsParams(timestamp)
	logger.WithFields(logrus.Fields{
		"newer_than": timestamp,
		"limit":      *params.Limit,
		"order":      *params.Order,
	}).Debug("Starting event processing for organization")

	totalEvents := 0
	validationErrors := 0
	totalPages := 0

	for {
		totalPages++
		pageLogger := logger.WithField("page_number", totalPages)

		// List the events since the last checkpoint for this organization
		events, status := e.handler.ListEvents(ctx, orgID, params)
		if status.Code != http.StatusOK {
			pageLogger.WithFields(logrus.Fields{
				"status_code": status.Code,
				"status_msg":  status.Message,
			}).Error("Failed to list events from API")
			return totalEvents, totalPages, validationErrors, fmt.Errorf("failed to list events: %s", status.Message)
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
				validationErrors++
				continue
			}

			if strings.TrimSpace(ev.InvolvedObject.Name) == "" {
				eventLogger.WithFields(logrus.Fields{
					"event_type": ev.Type,
					"reason":     ev.Reason,
				}).Warn("Skipping event: no involved object name")
				validationErrors++
				continue
			}

			eventLogger.Debug("Processing event")
			checkpointCtx.processEvent(ev, orgID)
		}

		if events.Metadata.Continue == nil {
			break // No more events to process
		}
		params.Continue = events.Metadata.Continue
	}

	return totalEvents, totalPages, validationErrors, nil
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

func (c *CheckpointContext) processEvent(event api.Event, orgID uuid.UUID) {
	switch event.Reason {
	case api.EventReasonResourceDeleted, api.EventReasonDeviceDecommissioned:
		c.resolveAllAlertsForResource(event, orgID)
	// Applications
	case api.EventReasonDeviceApplicationError:
		c.setAlert(event, string(api.EventReasonDeviceApplicationError), appStatusGroup, orgID)
	case api.EventReasonDeviceApplicationDegraded:
		c.setAlert(event, string(api.EventReasonDeviceApplicationDegraded), appStatusGroup, orgID)
	case api.EventReasonDeviceApplicationHealthy:
		c.clearAlertGroup(event, appStatusGroup, orgID)
	// CPU
	case api.EventReasonDeviceCPUCritical:
		c.setAlert(event, string(api.EventReasonDeviceCPUCritical), cpuGroup, orgID)
	case api.EventReasonDeviceCPUWarning:
		c.setAlert(event, string(api.EventReasonDeviceCPUWarning), cpuGroup, orgID)
	case api.EventReasonDeviceCPUNormal:
		c.clearAlertGroup(event, cpuGroup, orgID)
	// Memory
	case api.EventReasonDeviceMemoryCritical:
		c.setAlert(event, string(api.EventReasonDeviceMemoryCritical), memoryGroup, orgID)
	case api.EventReasonDeviceMemoryWarning:
		c.setAlert(event, string(api.EventReasonDeviceMemoryWarning), memoryGroup, orgID)
	case api.EventReasonDeviceMemoryNormal:
		c.clearAlertGroup(event, memoryGroup, orgID)
	// Disk
	case api.EventReasonDeviceDiskCritical:
		c.setAlert(event, string(api.EventReasonDeviceDiskCritical), diskGroup, orgID)
	case api.EventReasonDeviceDiskWarning:
		c.setAlert(event, string(api.EventReasonDeviceDiskWarning), diskGroup, orgID)
	case api.EventReasonDeviceDiskNormal:
		c.clearAlertGroup(event, diskGroup, orgID)
	// Device connection status
	case api.EventReasonDeviceDisconnected:
		c.setAlert(event, string(api.EventReasonDeviceDisconnected), nil, orgID)
	case api.EventReasonDeviceConnected:
		c.clearAlertGroup(event, []string{string(api.EventReasonDeviceDisconnected)}, orgID)
	}
}

func AlertKeyFromEvent(event api.Event, orgID uuid.UUID) AlertKey {
	return NewAlertKey(orgID.String(), event.InvolvedObject.Kind, event.InvolvedObject.Name)
}

func (c *CheckpointContext) resolveAllAlertsForResource(event api.Event, orgID uuid.UUID) {
	k := AlertKeyFromEvent(event, orgID)
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

func (c *CheckpointContext) setAlert(event api.Event, reason string, group []string, orgID uuid.UUID) {
	// Clear other alerts in the same group
	k := AlertKeyFromEvent(event, orgID)
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
		c.alerts[k][reason].OrgID = orgID.String()
		c.alerts[k][reason].Reason = reason
		c.alerts[k][reason].Summary = event.Message
		c.alerts[k][reason].StartsAt = *event.Metadata.CreationTimestamp
		c.alerts[k][reason].EndsAt = nil

		// Track if this is a new alert (not already active)
		if !alertExists {
			c.alertsCreated++
		}
	}
}

func (c *CheckpointContext) clearAlertGroup(event api.Event, group []string, orgID uuid.UUID) {
	k := AlertKeyFromEvent(event, orgID)
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
