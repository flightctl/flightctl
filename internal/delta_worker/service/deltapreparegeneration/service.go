package deltapreparegeneration

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
)

type Service interface {
	CreateDeltaPrepareGenerations(ctx context.Context, joins []*model.DeltaPrepareGeneration) (deltapreparegenerationstore.CreateDeltaPrepareGenerationsResult, error)
	ListDeltaPrepareGenerations(ctx context.Context, filter deltapreparegenerationstore.ListFilter) ([]model.DeltaPrepareGeneration, error)
}
