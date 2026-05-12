package service

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

func (h *ServiceHandler) DeleteDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	err := h.store.DependencyRef().DeleteByFleet(ctx, orgId, fleetName)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) DeleteDependencyRefsByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) domain.Status {
	err := h.store.DependencyRef().DeleteByDevice(ctx, orgId, deviceName)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceByFleet(ctx, orgId, fleetName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceDeviceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceDeviceRefsByFleet(ctx, orgId, fleetName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceFleetDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceByFleetDevice(ctx, orgId, fleetName, deviceName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceFleetScopedDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceFleetScopedDeviceRefs(ctx, orgId, deviceName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceStandaloneDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().ReplaceByStandaloneDevice(ctx, orgId, deviceName, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) BulkUpsertDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, refs []model.DependencyRef) domain.Status {
	err := h.store.DependencyRef().BulkUpsertDeviceRefs(ctx, orgId, refs)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDependencyRefsByRefType(ctx context.Context, orgId uuid.UUID, refType string) ([]model.DependencyRef, domain.Status) {
	refs, err := h.store.DependencyRef().ListByRefType(ctx, orgId, refType)
	return refs, StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDueGitDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, domain.Status) {
	probes, err := h.store.DependencyRef().ListDueGitDependencies(ctx, orgId, pollInterval)
	return probes, StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName string) ([]model.SecretDependencyRef, domain.Status) {
	refs, err := h.store.DependencyRef().ListSecretDependencyTargets(ctx, secretNamespace, secretName)
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

func (h *ServiceHandler) BulkUpsertSyncState(ctx context.Context, orgId uuid.UUID, states []model.SyncState) domain.Status {
	err := h.store.SyncState().BulkUpsert(ctx, orgId, states)
	return StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) BulkUpdateSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKeys []string, t time.Time) domain.Status {
	err := h.store.SyncState().BulkUpdateLastCheckedAt(ctx, orgId, resourceKeys, t)
	return StoreErrorToApiStatus(err, false, "", nil)
}
