package tasks

import (
	"context"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
)

const (
	// EventCleanupPollingInterval is the interval at which the event cleanup task runs.
	EventCleanupPollingInterval = 10 * time.Minute
)

type EventCleanup struct {
	log             logrus.FieldLogger
	serviceHandler  *service.ServiceHandler
	retentionPeriod util.Duration
}

func NewEventCleanup(log logrus.FieldLogger, serviceHandler *service.ServiceHandler, retentionPeriod util.Duration) *EventCleanup {
	return &EventCleanup{
		log:             log,
		serviceHandler:  serviceHandler,
		retentionPeriod: retentionPeriod,
	}
}

// Poll checks deletes events older than the configured retention period
func (t *EventCleanup) Poll() {
	t.log.Infof("Running EventCleanup Polling (retention period: %s)", t.retentionPeriod.String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cutoffTime := time.Now().Add(-time.Duration(t.retentionPeriod))
	numDeleted, status := t.serviceHandler.DeleteEventsOlderThan(ctx, cutoffTime)
	if status.Code != http.StatusOK {
		t.log.Errorf("failed to clean up events: %s", status.Message)
		return
	}
	t.log.Infof("cleaned up %d events", numDeleted)
}
