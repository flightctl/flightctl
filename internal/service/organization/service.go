package organization

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
)

type Service interface {
	ListOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status)
	ListAllOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status)

	// Internal sync APIs used by IdentityMapper (model types, not API list shapes).
	UpsertMany(ctx context.Context, orgs []*model.Organization) ([]*model.Organization, error)
	ListByIDs(ctx context.Context, ids []string) ([]*model.Organization, error)
	ListByExternalIDs(ctx context.Context, externalIDs []string) ([]*model.Organization, error)
	List(ctx context.Context, listParams store.ListParams) ([]*model.Organization, error)
}
