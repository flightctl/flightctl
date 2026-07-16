package templateversion

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateTemplateVersion(ctx context.Context, orgId uuid.UUID, templateVersion domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status)
	ListTemplateVersions(ctx context.Context, orgId uuid.UUID, fleet string, params domain.ListTemplateVersionsParams) (*domain.TemplateVersionList, domain.Status)
	GetTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*domain.TemplateVersion, domain.Status)
	DeleteTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) domain.Status
	GetLatestTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string) (*domain.TemplateVersion, domain.Status)
}
