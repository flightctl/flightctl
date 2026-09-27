package events

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store        eventstore.Store
	workerClient worker_client.WorkerClient
	log          logrus.FieldLogger
}

// NewServiceHandler creates a new events ServiceHandler instance.
func NewServiceHandler(store eventstore.Store, workerClient worker_client.WorkerClient, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{
		store:        store,
		workerClient: workerClient,
		log:          log,
	}
}

// SetWorkerClient wires the queue publisher after construction. Some workers
// construct their resource services before opening queue producers; keeping
// this setter on the concrete handler lets them enable event fan-out once the
// producer lifecycle is established.
func (h *ServiceHandler) SetWorkerClient(workerClient worker_client.WorkerClient) {
	h.workerClient = workerClient
}

var _ Service = (*ServiceHandler)(nil)

// CreateEvent creates an event in the store
func (h *ServiceHandler) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	if event == nil {
		return
	}

	err := h.store.Create(ctx, orgId, event)
	if err != nil {
		h.log.Errorf("failed emitting event <%s> (%s) for %s %s/%s: %v",
			*event.Metadata.Name, event.Reason, event.InvolvedObject.Kind, orgId, event.InvolvedObject.Name, err)
		return
	}

	if h.workerClient != nil {
		h.workerClient.EmitEvent(ctx, orgId, event)
	}
}

// CreateEventWithRetry persists an event and retries queue publication when a
// concrete worker client exposes publication errors. It is used by the
// standalone-device PrepareDeltas path, whose delayed render must not depend
// on a single transient queue write.
func (h *ServiceHandler) CreateEventWithRetry(ctx context.Context, orgId uuid.UUID, event *domain.Event) error {
	if event == nil {
		return nil
	}
	if err := h.store.Create(ctx, orgId, event); err != nil {
		return fmt.Errorf("create event: %w", err)
	}
	if h.workerClient == nil {
		return nil
	}
	reliable, ok := h.workerClient.(worker_client.ReliableWorkerClient)
	if !ok {
		h.workerClient.EmitEvent(ctx, orgId, event)
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return fmt.Errorf("publish event cancelled: %w", ctx.Err())
			case <-time.After(time.Duration(attempt) * 50 * time.Millisecond):
			}
		}
		if err := reliable.EmitEventWithError(ctx, orgId, event); err == nil {
			return nil
		} else {
			lastErr = err
		}
	}
	return fmt.Errorf("publish event after retries: %w", lastErr)
}

// HandleGenericResourceDeletedEvents handles generic resource deletion event emission logic
func (h *ServiceHandler) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedFailureEvent(ctx, resourceKind, name, status))
	} else {
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedSuccessEvent(ctx, resourceKind, name))
	}
}
