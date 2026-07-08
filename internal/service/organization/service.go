package organization

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
)

// Service is the focused Organization service interface, extracted from the monolithic
// internal/service.Service (internal/service/organization.go).
type Service interface {
	ListOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status)
}
