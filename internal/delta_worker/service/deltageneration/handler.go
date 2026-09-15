package deltageneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type prepareStore interface {
	deltastore.DeltaPrepareStore
}

type prepareGenerationStore interface {
	deltastore.DeltaPrepareGenerationStore
}

type ServiceHandler struct {
	store    deltastore.DeltaGenerationStore
	prepares prepareStore
	joins    prepareGenerationStore
	events   events.Service
	status   StatusService
	log      logrus.FieldLogger
}

func NewServiceHandler(store deltastore.DeltaGenerationStore, prepares prepareStore, joins prepareGenerationStore, eventService events.Service, status StatusService, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, prepares: prepares, joins: joins, events: eventService, status: status, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	if h.store == nil {
		return nil, fmt.Errorf("delta generation store is required")
	}
	current, err := h.store.InsertDeltaGenerations(ctx, generations)
	if err != nil {
		return nil, err
	}
	persisted := make(map[deltastore.GenerationKey]*model.DeltaGeneration, len(current))
	for i := range current {
		persisted[generationKeyOf(&current[i])] = &current[i]
	}
	for _, generation := range generations {
		if generation == nil || !isTerminalStatus(generation.Status) {
			continue
		}
		stored := persisted[generationKeyOf(generation)]
		if stored == nil || !isTerminalStatus(stored.Status) {
			continue
		}
		if err := h.emitForGeneration(ctx, stored, statusForGeneration(stored.Status), GenerationPhasePtr(stored)); err != nil {
			return nil, err
		}
	}
	return current, nil
}

func (h *ServiceHandler) GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return h.store.GetDeltaGeneration(ctx, key, opts...)
}

func (h *ServiceHandler) ListDeltaGenerations(ctx context.Context, keys []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	return h.store.ListDeltaGenerations(ctx, keys)
}

func (h *ServiceHandler) UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	updated, err := h.store.UpdateDeltaGeneration(ctx, expectedResourceVersion, generation)
	if err != nil {
		return nil, err
	}
	if err := h.emitForGeneration(ctx, updated, statusForGeneration(updated.Status), GenerationPhasePtr(updated)); err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *ServiceHandler) emitForGeneration(ctx context.Context, generation *model.DeltaGeneration, status domain.DeltaGenerationProgressDetailsGenerationStatus, phase *domain.DeltaGenerationPhase) error {
	if h.joins == nil || h.prepares == nil || h.events == nil || generation == nil {
		return nil
	}
	key := deltastore.GenerationKey{OrgID: generation.OrgID, ImageRepository: generation.ImageRepository, SourceDigest: generation.SourceDigest, TargetDigest: generation.TargetDigest}
	joins, err := h.joins.ListDeltaPrepareGenerations(ctx, deltastore.DeltaPrepareGenerationListFilter{GenerationKey: &key})
	if err != nil {
		return err
	}
	ids := make([]uuid.UUID, 0, len(joins))
	for _, join := range joins {
		ids = append(ids, join.PrepareID)
	}
	prepares, err := h.prepares.ListDeltaPrepares(ctx, ids)
	if err != nil {
		return err
	}
	for i := range prepares {
		prepare := &prepares[i]
		if prepare.Status != model.DeltaPrepareWaiting {
			continue
		}
		event, err := DeltaGenerationProgressEvent(ctx, *prepare, key, status, phase)
		if err != nil {
			return err
		}
		h.events.CreateEvent(ctx, prepare.OrgID, event)
		if h.status != nil && isTerminalStatus(generation.Status) {
			completed, total, err := h.prepares.CountDeltaPrepareGenerations(ctx, prepare.ID)
			if err != nil {
				return err
			}
			if total > 0 {
				if err := h.status.Set(ctx, prepare.OrgID, prepare.Kind, prepare.Name, completed, total); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func isTerminalStatus(status string) bool {
	return status == model.DeltaGenerationSucceeded || status == model.DeltaGenerationFailed || status == model.DeltaGenerationRejected
}

func generationKeyOf(generation *model.DeltaGeneration) deltastore.GenerationKey {
	return deltastore.GenerationKey{
		OrgID:           generation.OrgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}
}

func statusForGeneration(status string) domain.DeltaGenerationProgressDetailsGenerationStatus {
	switch status {
	case model.DeltaGenerationSucceeded:
		return domain.DeltaGenerationProgressSucceeded
	case model.DeltaGenerationFailed:
		return domain.DeltaGenerationProgressFailed
	case model.DeltaGenerationRejected:
		return domain.DeltaGenerationProgressRejected
	default:
		return domain.DeltaGenerationProgressInProgress
	}
}
