package alert_exporter

import (
	"context"
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
	alerts map[AlertKey]map[string]*AlertInfo
}

func (e *EventProcessor) ProcessLatestEvents(ctx context.Context, oldCheckpoint *AlertCheckpoint) (*AlertCheckpoint, error) {
	params := getListEventsParams(oldCheckpoint.Timestamp)

	checkpointCtx := CheckpointContext{
		alerts: oldCheckpoint.Alerts,
	}
	if checkpointCtx.alerts == nil {
		checkpointCtx.alerts = make(map[AlertKey]map[string]*AlertInfo)
	}
	for {
		// List the events since the last checkpoint
		events, status := e.handler.ListEvents(ctx, params)
		if status.Code != http.StatusOK {
			return nil, fmt.Errorf("Failed to list events: %s", status.Message)
		}

		for _, ev := range events.Items {
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
		return nil, fmt.Errorf("failed to get DB time: %s", status.Message)
	}

	return &AlertCheckpoint{Version: CurrentAlertCheckpointVersion, Alerts: checkpointCtx.alerts, Timestamp: timestamp.Format(time.RFC3339Nano)}, nil
}

func getListEventsParams(newerThan string) api.ListEventsParams {
	eventsOfInterest := []api.EventReason{
		api.DeviceApplicationDegraded,
		api.DeviceApplicationError,
		api.DeviceApplicationHealthy,
		api.DeviceCPUCritical,
		api.DeviceCPUNormal,
		api.DeviceCPUWarning,
		api.DeviceConnected,
		api.DeviceDisconnected,
		api.DeviceMemoryCritical,
		api.DeviceMemoryNormal,
		api.DeviceMemoryWarning,
		api.DeviceDiskCritical,
		api.DeviceDiskNormal,
		api.DeviceDiskWarning,
		api.ResourceDeleted,
		api.DeviceDecommissioned,
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
	appStatusGroup = []string{string(api.DeviceApplicationError), string(api.DeviceApplicationDegraded)}
	cpuGroup       = []string{string(api.DeviceCPUCritical), string(api.DeviceCPUWarning)}
	memoryGroup    = []string{string(api.DeviceMemoryCritical), string(api.DeviceMemoryWarning)}
	diskGroup      = []string{string(api.DeviceDiskCritical), string(api.DeviceDiskWarning)}
)

func (c *CheckpointContext) processEvent(event api.Event) {
	switch event.Reason {
	case api.ResourceDeleted, api.DeviceDecommissioned:
		c.resolveAllAlertsForResource(event)
	// Applications
	case api.DeviceApplicationError:
		c.setAlert(event, string(api.DeviceApplicationError), appStatusGroup)
	case api.DeviceApplicationDegraded:
		c.setAlert(event, string(api.DeviceApplicationDegraded), appStatusGroup)
	case api.DeviceApplicationHealthy:
		c.clearAlertGroup(event, appStatusGroup)
	// CPU
	case api.DeviceCPUCritical:
		c.setAlert(event, string(api.DeviceCPUCritical), cpuGroup)
	case api.DeviceCPUWarning:
		c.setAlert(event, string(api.DeviceCPUWarning), cpuGroup)
	case api.DeviceCPUNormal:
		c.clearAlertGroup(event, cpuGroup)
	// Memory
	case api.DeviceMemoryCritical:
		c.setAlert(event, string(api.DeviceMemoryCritical), memoryGroup)
	case api.DeviceMemoryWarning:
		c.setAlert(event, string(api.DeviceMemoryWarning), memoryGroup)
	case api.DeviceMemoryNormal:
		c.clearAlertGroup(event, memoryGroup)
	// Disk
	case api.DeviceDiskCritical:
		c.setAlert(event, string(api.DeviceDiskCritical), diskGroup)
	case api.DeviceDiskWarning:
		c.setAlert(event, string(api.DeviceDiskWarning), diskGroup)
	case api.DeviceDiskNormal:
		c.clearAlertGroup(event, diskGroup)
	// Device connection status
	case api.DeviceDisconnected:
		c.setAlert(event, string(api.DeviceDisconnected), nil)
	case api.DeviceConnected:
		c.clearAlertGroup(event, []string{string(api.DeviceDisconnected)})
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
			}
		}
	}

	// Set the alert if not already set
	if _, exists := c.alerts[k][reason]; !exists {
		c.alerts[k][reason] = &AlertInfo{}
	}
	if !c.alerts[k][reason].StartsAt.Equal(*event.Metadata.CreationTimestamp) {
		c.alerts[k][reason].ResourceName = event.InvolvedObject.Name
		c.alerts[k][reason].ResourceKind = event.InvolvedObject.Kind
		c.alerts[k][reason].OrgID = store.NullOrgId.String()
		c.alerts[k][reason].Reason = reason
		c.alerts[k][reason].StartsAt = *event.Metadata.CreationTimestamp
		c.alerts[k][reason].EndsAt = nil
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
			}
		}
	}
}
