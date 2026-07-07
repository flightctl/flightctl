package events

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// ServiceHandler implements Service. Narrowed from the monolithic EventHandler's
// store.Store aggregate dependency to the isolated internal/store/event.Store — the ONLY
// store accessor EventHandler ever called (h.store.Event().Create, in the original
// CreateEvent method).
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

// HandleGenericResourceDeletedEvents handles generic resource deletion event emission logic
func (h *ServiceHandler) HandleGenericResourceDeletedEvents(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, _, _ interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, string(resourceKind), &name)
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedFailureEvent(ctx, resourceKind, name, status))
	} else {
		h.CreateEvent(ctx, orgId, common.GetResourceDeletedSuccessEvent(ctx, resourceKind, name))
	}
}
