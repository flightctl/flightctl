package deltapreparegeneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
)

type ServiceHandler struct {
	store       deltastore.DeltaPrepareGenerationStore
	generations deltageneration.Service
	prepares    deltaprepare.Service
	events      events.Service
}

func NewServiceHandler(store deltastore.DeltaPrepareGenerationStore, generations deltageneration.Service, prepares deltaprepare.Service, eventService events.Service) *ServiceHandler {
	return &ServiceHandler{store: store, generations: generations, prepares: prepares, events: eventService}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) error {
	inserted, err := h.store.CreateDeltaPrepareGenerations(ctx, joins)
	if err != nil {
		return fmt.Errorf("create delta prepare generations: %w", err)
	}
	if h.generations == nil || h.prepares == nil || h.events == nil {
		return nil
	}
	keys := make([]deltastore.GenerationKey, 0, len(joins))
	for _, join := range inserted {
		keys = append(keys, deltastore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest})
	}
	generations, err := h.generations.ListDeltaGenerations(ctx, keys)
	if err != nil {
		return fmt.Errorf("list delta generations: %w", err)
	}
	byKey := make(map[deltastore.GenerationKey]*model.DeltaGeneration, len(generations))
	for i := range generations {
		generation := &generations[i]
		key := deltastore.GenerationKey{OrgID: generation.OrgID, ImageRepository: generation.ImageRepository, SourceDigest: generation.SourceDigest, TargetDigest: generation.TargetDigest}
		byKey[key] = generation
	}
	for _, join := range inserted {
		key := deltastore.GenerationKey{OrgID: join.OrgID, ImageRepository: join.ImageRepository, SourceDigest: join.SourceDigest, TargetDigest: join.TargetDigest}
		generation := byKey[key]
		if generation == nil || generation.Status != model.DeltaGenerationInProgress {
			continue
		}
		prepare, err := h.prepares.GetDeltaPrepare(ctx, deltastore.PrepareKey{ID: join.PrepareID})
		if err != nil {
			return fmt.Errorf("get delta prepare %s: %w", join.PrepareID, err)
		}
		if prepare == nil || prepare.Status != model.DeltaPrepareWaiting {
			continue
		}
		event, err := deltageneration.DeltaGenerationProgressEvent(ctx, *prepare, key, domain.DeltaGenerationProgressInProgress, deltageneration.GenerationPhasePtr(generation))
		if err != nil {
			return fmt.Errorf("create delta generation progress event: %w", err)
		}
		h.events.CreateEvent(ctx, prepare.OrgID, event)
	}
	return nil
}

func (h *ServiceHandler) ListDeltaPrepareGenerations(ctx context.Context, filter deltastore.DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error) {
	joins, err := h.store.ListDeltaPrepareGenerations(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("list delta prepare generations: %w", err)
	}
	return joins, nil
}
