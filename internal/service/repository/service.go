package repository

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateRepository(ctx context.Context, orgId uuid.UUID, repo domain.Repository) (*domain.Repository, domain.Status)
	ListRepositories(ctx context.Context, orgId uuid.UUID, params domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status)
	GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status)
	ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repo domain.Repository) (*domain.Repository, domain.Status)
	DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Repository, domain.Status)
	ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository domain.Repository, err error) (*domain.Repository, domain.Status)
	GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, domain.Status)
	GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, domain.Status)
	CheckRepositoryOciTag(ctx context.Context, orgId uuid.UUID, repositoryName, imageName, tag string) (*domain.OciRegistryCheckResult, domain.Status)
	CheckRepositoryOciImage(ctx context.Context, orgId uuid.UUID, repositoryName, imageName string) (*domain.OciRegistryCheckResult, domain.Status)
}
