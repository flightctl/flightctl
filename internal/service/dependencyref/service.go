package dependencyref

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

// Service is the focused DependencyRef service interface, extracted from the monolithic
// internal/service.Service. It covers the 12 DependencyRef methods defined in the old
// internal/service/dependency_ref.go (the SyncState half of that file belongs to the sibling
// internal/service/syncstate sub-package and is not part of this interface). Several methods
// take fleetName/deviceName parameters and are assigned here per the Feature design's §4.1
// cross-resource placement table, but none of the 12 method bodies reach into device.Store or
// fleet.Store — they call only their own dependencyrefstore.Store.
type Service interface {
	DeleteDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status
	DeleteDependencyRefsByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) domain.Status
	ReplaceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status
	ReplaceDeviceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status
	ReplaceFleetDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) domain.Status
	ReplaceFleetScopedDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status
	ReplaceStandaloneDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status
	BulkUpsertDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, refs []model.DependencyRef) domain.Status
	ListDependencyRefsByRefType(ctx context.Context, orgId uuid.UUID, refType string) ([]model.DependencyRef, domain.Status)
	ListDueGitDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, domain.Status)
	ListDueHttpDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.HttpDependencyProbe, domain.Status)
	ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, domain.Status)
}
