package deltapreparegeneration

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
)

type Service interface {
	CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) error
	ListDeltaPrepareGenerations(ctx context.Context, filter deltastore.DeltaPrepareGenerationListFilter) ([]model.DeltaPrepareGeneration, error)
}
