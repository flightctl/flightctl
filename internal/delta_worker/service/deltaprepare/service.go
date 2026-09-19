package deltaprepare

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	deltagenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	"github.com/google/uuid"
)

type Service interface {
	CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error
	CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (deltapreparestore.PrepareAdmission, error)
	GetDeltaPrepare(ctx context.Context, key deltapreparestore.PrepareKey, opts ...deltapreparestore.PrepareGetOption) (*model.DeltaPrepare, error)
	ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error)
	UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error)
	DecrementPendingGenerationsForGeneration(ctx context.Context, key deltagenerationstore.GenerationKey) ([]deltapreparestore.PrepareProgress, error)
	SetDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string, completed, total int) error
	ClearDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string) error
}

type StatusService = workerservice.PreparingStatus
