package organization

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
)

type Service interface {
	ListOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status)
	ListAllOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status)
}
