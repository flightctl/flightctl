package syncstate

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store/model"
	syncstatestore "github.com/flightctl/flightctl/internal/store/syncstate"
	"github.com/google/uuid"
)

// ServiceHandler implements Service. Holds only the store interface it actually uses — no
// `log`, no `events`; all 5 methods are pure store pass-throughs. Extracted from the SyncState
// half of internal/service/dependency_ref.go (the DependencyRef half is EDM-4666's; neither
// story edits the shared origin file — both only read from it).
type ServiceHandler struct {
	store syncstatestore.Store
}

// NewServiceHandler creates a new syncstate ServiceHandler instance.
func NewServiceHandler(store syncstatestore.Store) *ServiceHandler {
	return &ServiceHandler{store: store}
}

var _ Service = (*ServiceHandler)(nil)

// Every method here preserves a pre-existing behavior quirk of dependency_ref.go verbatim:
// StoreErrorToApiStatus is always called with an empty kind ("") and nil name, unlike every
// other resource file in internal/service (which passes a real domain.XKind and name). This is
// not "fixed" during extraction.

func (h *ServiceHandler) GetSyncState(ctx context.Context, orgId uuid.UUID, resourceKey string) (*model.SyncState, domain.Status) {
	state, err := h.store.Get(ctx, orgId, resourceKey)
	return state, common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) SetSyncState(ctx context.Context, orgId uuid.UUID, state *model.SyncState) domain.Status {
	err := h.store.Set(ctx, orgId, state)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) SetSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKey string, t time.Time) domain.Status {
	err := h.store.SetLastCheckedAt(ctx, orgId, resourceKey, t)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) BulkUpsertSyncState(ctx context.Context, orgId uuid.UUID, states []model.SyncState) domain.Status {
	err := h.store.BulkUpsert(ctx, orgId, states)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) BulkUpdateSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKeys []string, t time.Time) domain.Status {
	err := h.store.BulkUpdateLastCheckedAt(ctx, orgId, resourceKeys, t)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}
