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
	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
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
	checkpointManager := NewCheckpointManager(a.log, a.handler)
	eventProcessor := NewEventProcessor(a.log, a.handler)
	alertSender := NewAlertSender(a.log, a.config.Alertmanager.Hostname, a.config.Alertmanager.Port)

	ticker := time.NewTicker(time.Duration(a.config.Service.AlertPollingInterval))
	defer ticker.Stop()

	checkpoint := checkpointManager.LoadCheckpoint(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		func() {
			tickerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			checkpoint, err := eventProcessor.ProcessLatestEvents(tickerCtx, checkpoint)
			if err != nil {
				a.log.Errorf("failed processing events: %v", err)
				return
			}

			err = alertSender.SendAlerts(checkpoint)
			if err != nil {
				a.log.Errorf("failed sending alerts: %v", err)
				return
			}

			err = checkpointManager.StoreCheckpoint(tickerCtx, checkpoint)
			if err != nil {
				a.log.Errorf("failed storing checkpoint: %v", err)
				return
			}
		}()
	}

}
