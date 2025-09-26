package providers

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
)

// ExternalOrganizationProvider provides external organization information for a user.
type ExternalOrganizationProvider interface {
	// GetUserOrganizations returns all orgs a user is a member of.
	GetUserOrganizations(ctx context.Context, identity common.Identity) ([]org.ExternalOrganization, error)

	// IsMemberOf checks if a user is a member of a specific org.
	// externalOrgID should be the external organization ID as defined by the IdP.
	IsMemberOf(ctx context.Context, identity common.Identity, externalOrgID string) (bool, error)
}
