package service

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

func (h *ServiceHandler) ReplaceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceByFleet(ctx, orgId, fleetName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDependencyRefsByRefType(ctx context.Context, orgId uuid.UUID, refType string) ([]model.DependencyRef, domain.Status) {
	refs, err := h.store.DependencyRef().ListByRefType(ctx, orgId, refType)
	return refs, StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) GetSyncState(ctx context.Context, orgId uuid.UUID, resourceKey string) (*model.SyncState, domain.Status) {
	state, err := h.store.SyncState().Get(ctx, orgId, resourceKey)
	return state, StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) SetSyncState(ctx context.Context, orgId uuid.UUID, state *model.SyncState) domain.Status {
	err := h.store.SyncState().Set(ctx, orgId, state)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) SetSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKey string, t time.Time) domain.Status {
	err := h.store.SyncState().SetLastCheckedAt(ctx, orgId, resourceKey, t)
	return StoreErrorToApiStatus(err, false, "", nil)
}
