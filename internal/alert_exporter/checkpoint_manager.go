package alert_exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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
	checkpoint := &AlertCheckpoint{Version: CurrentAlertCheckpointVersion, Alerts: make(map[AlertKey]map[string]*AlertInfo)}

	checkpointBytes, status := c.handler.GetCheckpoint(ctx, AlertCheckpointConsumer, AlertCheckpointKey)
	if status.Code != http.StatusOK {
		if status.Code == http.StatusNotFound {
			c.log.Info("no alert checkpoint found")
		} else {
			c.log.Errorf("failed to get alert checkpoint: %v", status.Message)
		}
	}

	if status.Code == http.StatusOK && checkpointBytes != nil {
		if err := json.Unmarshal(checkpointBytes, checkpoint); err != nil {
			c.log.Errorf("failed to unmarshal alert checkpoint: %v", err)
		} else {
			c.log.Infof("resuming from last timestamp: %s", checkpoint.Timestamp)
		}
	}

	if checkpoint.Timestamp == "" {
		c.log.Info("starting with a fresh state")
	}

	return checkpoint
}

func (c *CheckpointManager) StoreCheckpoint(ctx context.Context, checkpoint *AlertCheckpoint) error {
	if checkpoint == nil {
		return fmt.Errorf("received nil checkpoint to store")
	}

	checkpointData, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("failed to marshal alert checkpoint: %v", err)
	}

	status := c.handler.SetCheckpoint(ctx, AlertCheckpointConsumer, AlertCheckpointKey, checkpointData)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to store checkpoint: %s", status.Message)
	}
	return nil
}
