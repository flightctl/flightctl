package worker_client

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type WorkerClient interface {
	EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event)
}

type EventWithOrgId struct {
	OrgId uuid.UUID    `json:"orgId"`
	Event domain.Event `json:"event"`
}

type workerClient struct {
	publisher queues.QueueProducer
	log       logrus.FieldLogger
}

func QueuePublisher(ctx context.Context, queuesProvider queues.Provider) (queues.QueueProducer, error) {
	publisher, err := queuesProvider.NewQueueProducer(ctx, consts.TaskQueue)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}
	return publisher, nil
}

func NewWorkerClient(publisher queues.QueueProducer, log logrus.FieldLogger) WorkerClient {
	return &workerClient{
		publisher: publisher,
		log:       log,
	}
}

func (t *workerClient) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	if !shouldEmitEvent(event.Reason) {
		return
	}

	b, err := json.Marshal(EventWithOrgId{
		OrgId: orgId,
		Event: *event,
	})
	if err != nil {
		t.log.WithError(err).Error("failed to marshal event for workers")
		return
	}
	// Use creation timestamp if available, otherwise use current time
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}

	if err = t.publisher.Enqueue(ctx, b, timestamp); err != nil {
		t.log.WithError(err).Error("failed to enqueue event for workers")
	}
}

// eventReasons contains all event reasons that should be sent to the workers
var eventReasons = map[domain.EventReason]struct{}{
	domain.EventReasonResourceCreated:             {},
	domain.EventReasonResourceUpdated:             {},
	domain.EventReasonResourceDeleted:             {},
	domain.EventReasonFleetRolloutStarted:         {},
	domain.EventReasonReferencedRepositoryUpdated: {},
	domain.EventReasonFleetRolloutDeviceSelected:  {},
	domain.EventReasonFleetRolloutBatchDispatched: {},
	domain.EventReasonDeviceConflictResolved:      {},
	domain.EventReasonDeviceDecommissioned:        {},
}

func shouldEmitEvent(reason domain.EventReason) bool {
	_, contains := eventReasons[reason]
	return contains
}
