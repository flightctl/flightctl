package providers

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/org"
)

// ExternalOrganizationProvider provides external organization information for a user
type ExternalOrganizationProvider interface {
	// GetUserOrganizations returns all orgs a user is a member of
	GetUserOrganizations(ctx context.Context, identity common.Identity) ([]org.ExternalOrganization, error)

	// IsMemberOf checks if a user is a member of a specific org
	IsMemberOf(ctx context.Context, identity common.Identity, orgID string) (bool, error)
}
