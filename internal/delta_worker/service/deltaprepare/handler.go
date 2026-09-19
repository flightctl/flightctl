package deltaprepare

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	eventservice "github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
)

type ServiceHandler struct {
	store  deltapreparestore.Store
	status *workerservice.StorePreparingStatus
	events eventservice.Service
}

func NewServiceHandler(store deltapreparestore.Store, status *workerservice.StorePreparingStatus) *ServiceHandler {
	return &ServiceHandler{store: store, status: status}
}

// NewCompletionService creates the delta-prepare service with the additional
// dependencies required by completion tasks. Completion remains on the same
// concrete service as prepare persistence; the separate constructor only
// makes the required resource services explicit at wiring time.
func NewCompletionService(
	store deltapreparestore.Store,
	status *workerservice.StorePreparingStatus,
	events eventservice.Service,
) (*ServiceHandler, error) {
	if store == nil {
		return nil, fmt.Errorf("delta prepare store is required")
	}
	if status == nil {
		return nil, fmt.Errorf("preparing status service is required")
	}
	if events == nil {
		return nil, fmt.Errorf("event service is required")
	}
	return &ServiceHandler{
		store:  store,
		status: status,
		events: events,
	}, nil
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error {
	if err := h.store.CreateDeltaPrepare(ctx, prepare); err != nil {
		return fmt.Errorf("create delta prepare: %w", err)
	}
	return nil
}

func (h *ServiceHandler) CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error) {
	admission, err := h.store.CreateOrReplaceWaitingDeltaPrepare(ctx, prepare)
	if err != nil {
		return deltapreparestore.PrepareAdmission{}, fmt.Errorf("create or replace waiting delta prepare: %w", err)
	}
	if admission.Replaced && h.status != nil {
		if err := h.status.Clear(ctx, prepare.OrgID, prepare.Kind, prepare.Name); err != nil {
			return deltapreparestore.PrepareAdmission{}, fmt.Errorf("clear replaced delta prepare status: %w", err)
		}
	}
	return admission, nil
}

func (h *ServiceHandler) GetDeltaPrepare(ctx context.Context, key deltapreparestore.PrepareKey, opts ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error) {
	prepare, err := h.store.GetDeltaPrepare(ctx, key, opts...)
	if err != nil {
		return nil, fmt.Errorf("get delta prepare: %w", err)
	}
	return prepare, nil
}

func (h *ServiceHandler) ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error) {
	prepares, err := h.store.ListDeltaPrepares(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list delta prepares: %w", err)
	}
	return prepares, nil
}

func (h *ServiceHandler) UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error) {
	updated, err := h.store.UpdateDeltaPrepare(ctx, expectedResourceVersion, prepare)
	if err != nil {
		return nil, fmt.Errorf("update delta prepare: %w", err)
	}
	return updated, nil
}

func (h *ServiceHandler) DecrementPendingGenerationsForGeneration(ctx context.Context, key deltagenerationstore.GenerationKey) ([]deltapreparestore.PrepareProgress, error) {
	progress, err := h.store.DecrementPendingGenerationsForGeneration(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("decrement pending generations for generation: %w", err)
	}
	return progress, nil
}

func (h *ServiceHandler) SetDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string, completed, total int) error {
	if h.status == nil {
		return nil
	}
	return h.status.Set(ctx, orgID, kind, name, completed, total)
}

func (h *ServiceHandler) ClearDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string) error {
	if h.status == nil {
		return nil
	}
	return h.status.Clear(ctx, orgID, kind, name)
}
