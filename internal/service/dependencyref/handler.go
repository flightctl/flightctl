package dependencyref

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store dependencyrefstore.Store
	log   logrus.FieldLogger
}

// NewServiceHandler creates a new dependencyref ServiceHandler instance.
func NewServiceHandler(store dependencyrefstore.Store, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, log: log}
}

var _ Service = (*ServiceHandler)(nil)

func (h *ServiceHandler) DeleteDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	err := h.store.DeleteByFleet(ctx, orgId, fleetName)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) DeleteDependencyRefsByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) domain.Status {
	err := h.store.DeleteByDevice(ctx, orgId, deviceName)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	err := h.store.ReplaceByFleet(ctx, orgId, fleetName, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceDeviceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	err := h.store.ReplaceDeviceRefsByFleet(ctx, orgId, fleetName, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceFleetDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.ReplaceByFleetDevice(ctx, orgId, fleetName, deviceName, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceFleetScopedDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.ReplaceFleetScopedDeviceRefs(ctx, orgId, deviceName, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ReplaceStandaloneDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	err := h.store.ReplaceByStandaloneDevice(ctx, orgId, deviceName, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) BulkUpsertDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, refs []model.DependencyRef) domain.Status {
	err := h.store.BulkUpsertDeviceRefs(ctx, orgId, refs)
	return common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDependencyRefsByRefType(ctx context.Context, orgId uuid.UUID, refType string) ([]model.DependencyRef, domain.Status) {
	refs, err := h.store.ListByRefType(ctx, orgId, refType)
	return refs, common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDueGitDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, domain.Status) {
	probes, err := h.store.ListDueGitDependencies(ctx, orgId, pollInterval)
	return probes, common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListDueHttpDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.HttpDependencyProbe, domain.Status) {
	probes, err := h.store.ListDueHttpDependencies(ctx, orgId, pollInterval)
	return probes, common.StoreErrorToApiStatus(err, false, "", nil)
}

func (h *ServiceHandler) ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, domain.Status) {
	refs, err := h.store.ListSecretDependencyTargets(ctx, secretNamespace, secretName, newFingerprint)
	return refs, common.StoreErrorToApiStatus(err, false, "", nil)
}
