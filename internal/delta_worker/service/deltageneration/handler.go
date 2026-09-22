package deltageneration

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/sirupsen/logrus"
)

// ServiceHandler owns persistence of delta-generation resources. Progress and
// completion fan-out are handled by the task that owns those workflows.
type ServiceHandler struct {
	store deltastore.Store
	log   logrus.FieldLogger
}

func NewServiceHandler(store deltastore.Store, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) CreateDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error) {
	return h.store.InsertDeltaGenerations(ctx, generations)
}

func (h *ServiceHandler) GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error) {
	return h.store.GetDeltaGeneration(ctx, key, opts...)
}

func (h *ServiceHandler) ListDeltaGenerations(ctx context.Context, keys []deltastore.GenerationKey) ([]model.DeltaGeneration, error) {
	return h.store.ListDeltaGenerations(ctx, keys)
}

func (h *ServiceHandler) UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error) {
	return h.store.UpdateDeltaGeneration(ctx, expectedResourceVersion, generation)
}
