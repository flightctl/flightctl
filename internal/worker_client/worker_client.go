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

type ClientOption func(*workerClient)

type workerClient struct {
	publisher      queues.QueueProducer
	deltaPublisher queues.QueueProducer
	log            logrus.FieldLogger
}

func QueuePublisher(ctx context.Context, queuesProvider queues.Provider) (queues.QueueProducer, error) {
	publisher, err := queuesProvider.NewQueueProducer(ctx, consts.TaskQueue)
	if err != nil {
		return nil, fmt.Errorf("failed to create publisher: %w", err)
	}
	return publisher, nil
}

// DeltaQueuePublisher returns a producer for the delta-generation task queue.
func DeltaQueuePublisher(ctx context.Context, queuesProvider queues.Provider) (queues.QueueProducer, error) {
	publisher, err := queuesProvider.NewQueueProducer(ctx, consts.DeltaGenerationTaskQueue)
	if err != nil {
		return nil, fmt.Errorf("failed to create delta publisher: %w", err)
	}
	return publisher, nil
}

// WithDeltaPublisher configures a worker client to publish delta-generation events.
func WithDeltaPublisher(p queues.QueueProducer) ClientOption {
	return func(c *workerClient) {
		c.deltaPublisher = p
	}
}

func NewWorkerClient(publisher queues.QueueProducer, log logrus.FieldLogger, opts ...ClientOption) WorkerClient {
	c := &workerClient{
		publisher: publisher,
		log:       log,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (t *workerClient) EmitEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}
	if _, isDelta := deltaEventReasons[event.Reason]; isDelta {
		if t.deltaPublisher == nil {
			t.log.WithFields(logrus.Fields{
				"orgId":  orgId,
				"reason": event.Reason,
			}).Warn("delta-generation publisher is not configured; dropping event")
			return
		}
		t.enqueue(ctx, orgId, event, t.deltaPublisher)
		return
	}
	if !shouldEmitEvent(event.Reason) {
		return
	}
	t.enqueue(ctx, orgId, event, t.publisher)
}

// EnqueueEvent marshals event and writes it to producer. It returns marshal and enqueue errors.
func EnqueueEvent(ctx context.Context, producer queues.QueueProducer, orgId uuid.UUID, event *domain.Event) error {
	if event == nil {
		return fmt.Errorf("event is required")
	}
	if producer == nil {
		return fmt.Errorf("queue producer is required")
	}
	b, err := json.Marshal(EventWithOrgId{
		OrgId: orgId,
		Event: *event,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal event for workers: %w", err)
	}
	var timestamp int64
	if event.Metadata.CreationTimestamp != nil {
		timestamp = event.Metadata.CreationTimestamp.UnixMicro()
	} else {
		timestamp = time.Now().UnixMicro()
	}
	if err = producer.Enqueue(ctx, b, timestamp); err != nil {
		return fmt.Errorf("failed to enqueue event for workers: %w", err)
	}
	return nil
}

func (t *workerClient) enqueue(ctx context.Context, orgId uuid.UUID, event *domain.Event, producer queues.QueueProducer) {
	if producer == nil {
		return
	}
	if err := EnqueueEvent(ctx, producer, orgId, event); err != nil {
		t.log.WithError(err).Error("failed to enqueue event for workers")
	}
}

var eventReasons = map[domain.EventReason]struct{}{
	domain.EventReasonResourceCreated:             {},
	domain.EventReasonResourceUpdated:             {},
	domain.EventReasonResourceDeleted:             {},
	domain.EventReasonFleetRolloutStarted:         {},
	domain.EventReasonReferencedRepositoryUpdated: {},
	domain.EventReasonDependencyChangeDetected:    {},
	domain.EventReasonFleetRolloutDeviceSelected:  {},
	domain.EventReasonFleetRolloutBatchDispatched: {},
	domain.EventReasonDeviceConflictResolved:      {},
	domain.EventReasonDeviceDecommissioned:        {},
	domain.EventReasonApplicationLifecycleChanged: {},
	domain.EventReasonDeltaGenerationCompleted:    {},
}

var deltaEventReasons = map[domain.EventReason]struct{}{
	domain.EventReasonPrepareDeltas: {},
	domain.EventReasonGenerateDelta: {},
}

// IsDeltaGenerationQueueEvent reports whether reason belongs on DeltaGenerationTaskQueue.
func IsDeltaGenerationQueueEvent(reason domain.EventReason) bool {
	_, ok := deltaEventReasons[reason]
	return ok
}

func shouldEmitEvent(reason domain.EventReason) bool {
	_, contains := eventReasons[reason]
	return contains
}
