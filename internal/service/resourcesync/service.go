package resourcesync

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status)
	ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params domain.ListResourceSyncsParams) (*domain.ResourceSyncList, domain.Status)
	GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, domain.Status)
	ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status)
	DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) domain.Status
	PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.ResourceSync, domain.Status)
	ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync domain.ResourceSync) (*domain.ResourceSync, domain.Status)
}
