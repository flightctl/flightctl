package service

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
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

	if !IsInternalRequest(ctx) {
		var listErr error
		userOrgs, listErr := h.listUserOrganizations(ctx)
		if listErr != nil {
			status := StoreErrorToApiStatus(listErr, false, api.OrganizationKind, nil)
			return nil, status
		}
		if params.FieldSelector != nil && *params.FieldSelector != "" {
			allowedIDs := parseFieldSelectorForOrgIDs(*params.FieldSelector)

			filtered := make([]*model.Organization, 0, len(userOrgs))
			for _, userOrg := range userOrgs {
				if _, exists := allowedIDs[userOrg.ID]; exists {
					filtered = append(filtered, userOrg)
				}
			}
			orgs = filtered
		} else {
			orgs = userOrgs
		}

	} else {
		orgs, err = h.store.Organization().List(ctx, *listParams)
		if err != nil {
			status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
			return nil, status
		}
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

func parseFieldSelectorForOrgIDs(selectorStr string) map[uuid.UUID]struct{} {
	selectorRegex := regexp.MustCompile(`["']?([0-9a-fA-F-]{36})["']?`)

	allowedIDs := make(map[uuid.UUID]struct{})
	matches := selectorRegex.FindAllStringSubmatch(selectorStr, -1)

	for _, match := range matches {
		if id, err := uuid.Parse(match[1]); err == nil {
			allowedIDs[id] = struct{}{}
		}
	}
	return allowedIDs
}
