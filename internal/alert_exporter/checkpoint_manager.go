package alert_exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/service"
	"github.com/sirupsen/logrus"
)

const AlertCheckpointConsumer = "alert-exporter"
const AlertCheckpointKey = "active-alerts"

type CheckpointManager struct {
	log     *logrus.Logger
	handler service.Service
}

func NewCheckpointManager(log *logrus.Logger, handler service.Service) *CheckpointManager {
	return &CheckpointManager{
		log:     log,
		handler: handler,
	}
}

// LoadCheckpoint retrieves the last processed event and active alerts from the database.
// If no checkpoint exists, it initializes a fresh state.
// If it fails to retrieve the checkpoint or unmarshal the contents, it logs an error and starts
// from a fresh state. This is better than panicking, as it allows the exporter to continue running
// and at least report new alerts from the point of failure onward.
// In the future, we could consider using a more robust error handling strategy, such as listing
// the system resources and reconstructing the list of active alerts based on the current state
// of the system. However, for now, I assume that if we fail to fetch the checkpoint then we will
// also fail to fetch the system resources.
func (c *CheckpointManager) LoadCheckpoint(ctx context.Context) *AlertCheckpoint {
	logger := c.log.WithFields(logrus.Fields{
		"component": "checkpoint_manager",
		"operation": "load",
	})

	logger.Debug("Loading alert checkpoint")

	checkpointData, status := c.handler.GetCheckpoint(ctx, AlertCheckpointConsumer, AlertCheckpointKey)
	if status.Code == http.StatusNotFound {
		CheckpointOperationsTotal.WithLabelValues("load", "not_found").Inc()
		logger.Info("No existing alert checkpoint found, starting with fresh state")
		return &AlertCheckpoint{
			Version:   CurrentAlertCheckpointVersion,
			Alerts:    make(map[AlertKey]map[string]*AlertInfo),
			Timestamp: time.Now().Add(-time.Hour).Format(time.RFC3339Nano), // Start from 1 hour ago
		}
	}
	if status.Code != http.StatusOK {
		CheckpointOperationsTotal.WithLabelValues("load", "error").Inc()
		logger.WithFields(logrus.Fields{
			"status_code": status.Code,
			"status_msg":  status.Message,
		}).Error("Failed to get alert checkpoint from storage")
		return &AlertCheckpoint{
			Version:   CurrentAlertCheckpointVersion,
			Alerts:    make(map[AlertKey]map[string]*AlertInfo),
			Timestamp: time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
		}
	}

	var checkpoint AlertCheckpoint
	err := json.Unmarshal(checkpointData, &checkpoint)
	if err != nil {
		CheckpointOperationsTotal.WithLabelValues("load", "unmarshal_error").Inc()
		logger.WithFields(logrus.Fields{
			"error":              err,
			"checkpoint_size":    len(checkpointData),
			"checkpoint_preview": string(checkpointData[:min(100, len(checkpointData))]),
		}).Error("Failed to unmarshal alert checkpoint")
		return &AlertCheckpoint{
			Version:   CurrentAlertCheckpointVersion,
			Alerts:    make(map[AlertKey]map[string]*AlertInfo),
			Timestamp: time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
		}
	}

	CheckpointOperationsTotal.WithLabelValues("load", "success").Inc()
	CheckpointSizeBytes.Set(float64(len(checkpointData)))

	logger.WithFields(logrus.Fields{
		"checkpoint_version":   checkpoint.Version,
		"checkpoint_timestamp": checkpoint.Timestamp,
		"alert_keys":           len(checkpoint.Alerts),
		"total_alerts":         c.countTotalAlerts(checkpoint.Alerts),
	}).Info("Successfully loaded alert checkpoint")

	return &checkpoint
}

func (c *CheckpointManager) StoreCheckpoint(ctx context.Context, checkpoint *AlertCheckpoint) error {
	logger := c.log.WithFields(logrus.Fields{
		"component":            "checkpoint_manager",
		"operation":            "store",
		"checkpoint_version":   checkpoint.Version,
		"checkpoint_timestamp": checkpoint.Timestamp,
		"alert_keys":           len(checkpoint.Alerts),
		"total_alerts":         c.countTotalAlerts(checkpoint.Alerts),
	})

	logger.Debug("Storing alert checkpoint")

	data, err := json.Marshal(checkpoint)
	if err != nil {
		CheckpointOperationsTotal.WithLabelValues("store", "marshal_error").Inc()
		logger.WithFields(logrus.Fields{
			"error": err,
		}).Error("Failed to marshal checkpoint for storage")
		return fmt.Errorf("failed to marshal checkpoint: %v", err)
	}

	logger.WithFields(logrus.Fields{
		"checkpoint_size": len(data),
	}).Debug("Checkpoint marshaled successfully")

	status := c.handler.SetCheckpoint(ctx, AlertCheckpointConsumer, AlertCheckpointKey, data)
	if status.Code != http.StatusOK {
		CheckpointOperationsTotal.WithLabelValues("store", "error").Inc()
		logger.WithFields(logrus.Fields{
			"status_code":     status.Code,
			"status_msg":      status.Message,
			"checkpoint_size": len(data),
		}).Error("Failed to store checkpoint")
		return fmt.Errorf("failed to store checkpoint: %s", status.Message)
	}

	CheckpointOperationsTotal.WithLabelValues("store", "success").Inc()
	CheckpointSizeBytes.Set(float64(len(data)))

	logger.Debug("Alert checkpoint stored successfully")
	return nil
}

// countTotalAlerts counts the total number of alerts across all resources
func (c *CheckpointManager) countTotalAlerts(alerts map[AlertKey]map[string]*AlertInfo) int {
	total := 0
	for _, reasons := range alerts {
		total += len(reasons)
	}
	return total
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
