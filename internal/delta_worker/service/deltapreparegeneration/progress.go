package deltapreparegeneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
)

// ProgressHandler owns lookups through DeltaPrepareGeneration and progress
// event emission for nonterminal phases of linked prepares. Terminal progress
// is emitted by the generation-complete task while it processes the prepares
// it already claimed.
type ProgressHandler struct {
	joins    deltapreparegenerationstore.Store
	prepares deltaprepare.Service
	events   events.Service
}

func NewProgressHandler(
	joins deltapreparegenerationstore.Store,
	prepares deltaprepare.Service,
	eventService events.Service,
) *ProgressHandler {
	return &ProgressHandler{joins: joins, prepares: prepares, events: eventService}
}

func (h *ProgressHandler) EmitForGeneration(
	ctx context.Context,
	generation *model.DeltaGeneration,
) error {
	if h.joins == nil || h.prepares == nil || h.events == nil || generation == nil {
		return nil
	}
	if generation.Status != model.DeltaGenerationInProgress {
		return nil
	}
	key := generationKeyOf(generation)
	joins, err := h.joins.ListDeltaPrepareGenerations(ctx, deltapreparegenerationstore.ListFilter{GenerationKey: &key})
	if err != nil {
		return fmt.Errorf("list delta prepare generations for progress: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(joins))
	for _, join := range joins {
		ids = append(ids, join.PrepareID)
	}
	prepares, err := h.prepares.ListDeltaPrepares(ctx, ids)
	if err != nil {
		return fmt.Errorf("list prepares for delta generation progress: %w", err)
	}
	for i := range prepares {
		prepare := &prepares[i]
		if prepare.Status != model.DeltaPrepareWaiting {
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

func generationKeyOf(generation *model.DeltaGeneration) deltagenerationstore.GenerationKey {
	return deltagenerationstore.GenerationKey{
		OrgID:           generation.OrgID,
		ImageRepository: generation.ImageRepository,
		SourceDigest:    generation.SourceDigest,
		TargetDigest:    generation.TargetDigest,
	}
}
