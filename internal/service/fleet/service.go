package fleet

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateFleet(ctx context.Context, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status)
	ListFleets(ctx context.Context, orgId uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status)
	GetFleet(ctx context.Context, orgId uuid.UUID, name string, params domain.GetFleetParams) (*domain.Fleet, domain.Status)
	ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet, enforceOwnership bool) (*domain.Fleet, domain.Status)
	DeleteFleet(ctx context.Context, orgId uuid.UUID, name string, enforceOwnership bool) domain.Status
	GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, domain.Status)
	ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status)
	PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest, enforceOwnership bool) (*domain.Fleet, domain.Status)
	ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status)
	ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status)
	UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status
	UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status
	OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status
	GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status)
	StopFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status)
	StartFleetApplication(ctx context.Context, orgId uuid.UUID, name string, appName string) (*domain.Fleet, domain.Status)
}
