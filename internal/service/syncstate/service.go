package syncstate

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

// Service is the focused SyncState service interface, extracted from the SyncState half of
// the monolithic internal/service.Service (internal/service/dependency_ref.go). The
// DependencyRef half of that file is extracted separately, by EDM-4666.
type Service interface {
	GetSyncState(ctx context.Context, orgId uuid.UUID, resourceKey string) (*model.SyncState, domain.Status)
	SetSyncState(ctx context.Context, orgId uuid.UUID, state *model.SyncState) domain.Status
	SetSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKey string, t time.Time) domain.Status
	BulkUpsertSyncState(ctx context.Context, orgId uuid.UUID, states []model.SyncState) domain.Status
	BulkUpdateSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKeys []string, t time.Time) domain.Status
}
