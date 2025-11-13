package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/store/model"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", api.APIGroup, api.OrganizationAPIVersion)

func (h *ServiceHandler) ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	var orgs []*model.Organization
	var err error
	if IsInternalRequest(ctx) {
		orgs, err = h.listAllOrganizations(ctx)
	} else {
		orgs, err = h.listUserOrganizations(ctx)
	}

	status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
	if err != nil {
		return nil, status
	}

	apiOrgs := make([]api.Organization, len(orgs))
	for i, org := range orgs {
		name := org.ID.String()
		apiOrgs[i] = api.Organization{
			ApiVersion: organizationApiVersion,
			Kind:       api.OrganizationKind,
			Metadata:   api.ObjectMeta{Name: &name},
			Spec: &api.OrganizationSpec{
				ExternalId:  &org.ExternalID,
				DisplayName: &org.DisplayName,
			},
		}
	}

	return &api.OrganizationList{
		Items:      apiOrgs,
		ApiVersion: organizationApiVersion,
		Kind:       api.OrganizationListKind,
		Metadata:   api.ListMeta{},
	}, status
}

func (h *ServiceHandler) listAllOrganizations(ctx context.Context) ([]*model.Organization, error) {
	orgs, err := h.store.Organization().List(ctx)
	if err != nil {
		return nil, err
	}

	return orgs, nil
}

func (h *ServiceHandler) listUserOrganizations(ctx context.Context) ([]*model.Organization, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no mapped identity found in context")
	}

	return mappedIdentity.Organizations, nil
}
