package service

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/store/model"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", api.APIGroup, api.OrganizationAPIVersion)

// organizationModelToAPI converts a model.Organization to api.Organization
func organizationModelToAPI(org *model.Organization) api.Organization {
	name := org.ID.String()
	return api.Organization{
		ApiVersion: organizationApiVersion,
		Kind:       api.OrganizationKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec: &api.OrganizationSpec{
			ExternalId:  &org.ExternalID,
			DisplayName: &org.DisplayName,
		},
	}
}

func (h *ServiceHandler) ListOrganizations(ctx context.Context, params api.ListOrganizationsParams) (*api.OrganizationList, api.Status) {
	var orgs []*model.Organization
	var err error
	listParams, status := prepareListParams(nil, nil, params.FieldSelector, nil)
	if status.Code != http.StatusOK {
		return nil, status
	}

	orgs, err = h.store.Organization().List(ctx, *listParams)
	if err != nil {
		status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
		return nil, status
	}

	if !IsInternalRequest(ctx) {
		userOrgs, err := h.listUserOrganizations(ctx)
		if err != nil {
			status = StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
			return nil, status
		}

		userOrgSet := make(map[string]struct{}, len(userOrgs))
		for _, m := range userOrgs {
			userOrgSet[m.ID.String()] = struct{}{}
		}

		filtered := make([]*model.Organization, 0)
		for _, org := range orgs {
			if _, ok := userOrgSet[org.ID.String()]; ok {
				filtered = append(filtered, org)
			}
		}
		orgs = filtered
	}

	apiOrgs := make([]api.Organization, len(orgs))
	for i, org := range orgs {
		apiOrgs[i] = organizationModelToAPI(org)
	}

	return &api.OrganizationList{
		Items:      apiOrgs,
		ApiVersion: organizationApiVersion,
		Kind:       api.OrganizationListKind,
		Metadata:   api.ListMeta{},
	}, api.StatusOK()
}

func (h *ServiceHandler) listUserOrganizations(ctx context.Context) ([]*model.Organization, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("no mapped identity found in context")
	}

	return mappedIdentity.Organizations, nil
}
