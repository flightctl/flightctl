package deltaprepare

import (
	"context"

	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	"github.com/google/uuid"
)

type Service interface {
	CreateDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) error
	CreateOrReplaceWaitingDeltaPrepare(ctx context.Context, prepare *model.DeltaPrepare) (deltastore.PrepareAdmission, error)
	GetDeltaPrepare(ctx context.Context, key deltastore.PrepareKey, opts ...deltastore.PrepareGetOption) (*model.DeltaPrepare, error)
	ListDeltaPrepares(ctx context.Context, ids []uuid.UUID) ([]model.DeltaPrepare, error)
	UpdateDeltaPrepare(ctx context.Context, expectedResourceVersion int64, prepare *model.DeltaPrepare) (*model.DeltaPrepare, error)
	CountDeltaPrepareGenerations(ctx context.Context, prepareID uuid.UUID) (completed, total int, err error)
	SetDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string, completed, total int) error
	ClearDeltaPreparingStatus(ctx context.Context, orgID uuid.UUID, kind, name string) error
}

type StatusService = workerservice.PreparingStatus
