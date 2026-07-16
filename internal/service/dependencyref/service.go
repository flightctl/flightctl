package dependencyref

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

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
