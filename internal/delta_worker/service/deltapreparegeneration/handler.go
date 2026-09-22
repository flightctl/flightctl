package deltapreparegeneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
)

type ServiceHandler struct {
	store deltapreparegenerationstore.Store
}

func NewServiceHandler(store deltapreparegenerationstore.Store) *ServiceHandler {
	return &ServiceHandler{store: store}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error) {
	created, err := h.store.CreateDeltaPrepareGenerations(ctx, joins)
	if err != nil {
		return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, fmt.Errorf("create delta prepare generations: %w", err)
	}
	return created, nil
}

func (h *ServiceHandler) ListDeltaPrepareGenerations(ctx context.Context, filter deltapreparegenerationstore.ListFilter) ([]model.DeltaPrepareGeneration, error) {
	joins, err := h.store.ListDeltaPrepareGenerations(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list delta prepare generations: %w", err)
	}
	return joins, nil
}
