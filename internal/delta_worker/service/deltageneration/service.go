package deltageneration

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
)

type Service interface {
	CreateDeltaGenerations(ctx context.Context, generations []*model.DeltaGeneration) ([]model.DeltaGeneration, error)
	GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*model.DeltaGeneration, error)
	ListDeltaGenerations(ctx context.Context, keys []deltastore.GenerationKey) ([]model.DeltaGeneration, error)
	UpdateDeltaGeneration(ctx context.Context, expectedResourceVersion int64, generation *model.DeltaGeneration) (*model.DeltaGeneration, error)
}
