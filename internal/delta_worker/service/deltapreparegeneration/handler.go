package deltapreparegeneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
)

type ServiceHandler struct {
	store       deltapreparegenerationstore.Store
	generations deltageneration.Service
	events      events.Service
}

func NewServiceHandler(store deltapreparegenerationstore.Store, generations deltageneration.Service, eventService events.Service) *ServiceHandler {
	return &ServiceHandler{store: store, generations: generations, events: eventService}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error) {
	created, err := h.store.CreateDeltaPrepareGenerations(ctx, joins)
	if err != nil {
		return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, fmt.Errorf("create delta prepare generations: %w", err)
	}
	if h.generations == nil || h.events == nil {
		return created, nil
	}
	keys := make([]deltagenerationstore.GenerationKey, 0, len(created.InsertedJoins))
	for _, join := range created.InsertedJoins {
		keys = append(keys, deltagenerationstore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest})
	}
	generations, err := h.generations.ListDeltaGenerations(ctx, keys)
	if err != nil {
		return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, fmt.Errorf("list delta generations: %w", err)
	}
	byKey := make(map[deltagenerationstore.GenerationKey]*model.DeltaGeneration, len(generations))
	for i := range generations {
		generation := &generations[i]
		key := deltagenerationstore.GenerationKey{OrgID: generation.OrgID, ImageRepository: generation.ImageRepository, SourceDigest: generation.SourceDigest, TargetDigest: generation.TargetDigest}
		byKey[key] = generation
	}
	for _, join := range created.InsertedJoins {
		key := deltagenerationstore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest}
		generation := byKey[key]
		if generation == nil || generation.Status != model.DeltaGenerationInProgress {
			continue
		}
		prepare, ok := created.UpdatedPrepares[join.PrepareID]
		if !ok || prepare.Status != model.DeltaPrepareWaiting {
			continue
		}
		event, err := deltageneration.DeltaGenerationProgressEvent(ctx, prepare, key, domain.DeltaGenerationProgressInProgress, deltageneration.GenerationPhasePtr(generation))
		if err != nil {
			return deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult{}, fmt.Errorf("create delta generation progress event: %w", err)
		}
		h.events.CreateEvent(ctx, prepare.OrgID, event)
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
