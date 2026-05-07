package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

func (h *ServiceHandler) UpsertDependencyRef(ctx context.Context, orgId uuid.UUID, ref *model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().Upsert(ctx, orgId, ref)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) DeleteDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	err := h.store.DependencyRef().DeleteByFleet(ctx, orgId, fleetName)
	return StoreErrorToApiStatus(err, false, "", nil)
}
