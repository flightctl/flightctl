package providers

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/org"
)

const organizationClaimName = "organization"
const organizationClaimID = "id"

// OrgName and OrgID are internal aliases used for readability within the provider.
type orgName string
type orgID string

type ClaimsProvider struct{}

func (c *ClaimsProvider) GetUserOrganizations(ctx context.Context, identity common.Identity) ([]org.ExternalOrganization, error) {
	orgs, err := claimsFromIdentity(identity)
	if err != nil {
		return nil, err
	}

	externalOrgs := make([]org.ExternalOrganization, 0, len(orgs))
	for id, name := range orgs {
		externalOrgs = append(externalOrgs, org.ExternalOrganization{
			ID:   string(id),
			Name: string(name),
		})
	}

	return externalOrgs, nil
}

func (c *ClaimsProvider) IsMemberOf(ctx context.Context, identity common.Identity, externalOrgID string) (bool, error) {
	orgs, err := claimsFromIdentity(identity)
	if err != nil {
		return false, err
	}

	if _, ok := orgs[orgID(externalOrgID)]; ok {
		return true, nil
	}

	return false, nil
}

// claimsFromIdentity returns a map of orgID -> orgName
// pulled from the claims of the token identity
func claimsFromIdentity(identity common.Identity) (map[orgID]orgName, error) {
	tokenIdentity, ok := identity.(authn.TokenIdentity)
	if !ok {
		return nil, fmt.Errorf("cannot get organizations claims from a non-token identity")
	}

	organizationClaims, ok := tokenIdentity.GetClaim(organizationClaimName)
	if !ok {
		return nil, fmt.Errorf("%w: %s claim not found", flterrors.ErrMissingTokenClaims, organizationClaimName)
	}

	// Organization claims are a map in the format of
	// {
	//   "organization-name": {
	// 	   "id": "organization-unique-identifier"
	// 	 }
	//   ...
	// }
	rawOrgs, ok := organizationClaims.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%w: invalid organizations claim format", flterrors.ErrInvalidTokenClaims)
	}

	// Transform the claims into a map of orgID -> orgName
	orgMap := make(map[orgID]orgName)
	for orgNameStr, orgClaimData := range rawOrgs {
		orgClaimMap, ok := orgClaimData.(map[string]interface{})
		if !ok {
			continue // Skip entries that aren't maps
		}

		orgIDValue, exists := orgClaimMap[organizationClaimID]
		if !exists {
			continue // Skip entries without ID
		}

		// Extract the ID as a string
		orgIDStr, ok := orgIDValue.(string)
		if !ok {
			continue // Skip entries where ID is not a string
		}

		orgMap[orgID(orgIDStr)] = orgName(orgNameStr)
	}

	return orgMap, nil
}
