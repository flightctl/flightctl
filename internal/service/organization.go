package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

const OrganizationKind = "Organization"

var organizationApiVersion = fmt.Sprintf("%s/%s", api.APIGroup, api.OrganizationAPIVersion)

func (h *ServiceHandler) ListUserOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	orgs, err := h.store.Organization().List(ctx)
	status := StoreErrorToApiStatus(err, false, OrganizationKind, nil)
	if err != nil {
		return nil, status
	}

	apiOrgs := make([]api.Organization, len(orgs))
	for i, org := range orgs {
		// In the future, displayName will be populated from information from the IdP.
		// For now, there should only be the default organization so any others that might exist are unknown.
		displayName := "Unknown"
		if org.Default {
			displayName = "Default"
		}

		apiOrgs[i] = api.Organization{
			ApiVersion:  organizationApiVersion,
			Kind:        api.OrganizationKind,
			Id:          org.ID,
			DisplayName: displayName,
		}
	}

	return &api.OrganizationList{
		Items:      apiOrgs,
		ApiVersion: organizationApiVersion,
		Kind:       api.OrganizationListKind,
		Metadata:   api.ListMeta{},
	}, status
}
