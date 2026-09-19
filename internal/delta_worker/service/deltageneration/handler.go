package deltageneration

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store    deltastore.Store
	progress ProgressFunc
	log      logrus.FieldLogger
}

func NewServiceHandler(store deltastore.Store, progress ProgressFunc, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, progress: progress, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	if h.store == nil {
		return nil, fmt.Errorf("delta generation store is required")
	}
	return h.store.InsertDeltaGenerations(ctx, generations)
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
	if err := h.emitProgress(ctx, updated); err != nil {
		return nil, err
	}
	return updated, nil
}

func (h *ServiceHandler) emitProgress(ctx context.Context, generation *model.DeltaGeneration) error {
	if h.progress == nil || generation == nil || isTerminalStatus(generation.Status) {
		return nil
	}
	return h.progress(ctx, generation, statusForGeneration(generation.Status), GenerationPhasePtr(generation))
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
