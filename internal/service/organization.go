package service

import (
	"context"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", api.APIGroup, api.OrganizationAPIVersion)

func (h *ServiceHandler) ListOrganizations(ctx context.Context, params api.ListOrganizationsParams) (*api.OrganizationList, api.Status) {
	var orgs []*model.Organization
	var err error
	listParams, status := prepareListParams(params.Continue, nil, params.FieldSelector, params.Limit)
	if status.Code != http.StatusOK {
		return nil, status
	}

	if IsInternalRequest(ctx) {
		orgs, err = h.store.Organization().List(ctx, *listParams)
		status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
		if err != nil {
			return nil, status
		}
	} else {

		userOrgs, err := h.listUserOrganizations(ctx)
		if err != nil {
			status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
			return nil, status
		}

		userOrgSet := make(map[string]struct{}, len(userOrgs))
		for _, m := range userOrgs {
			userOrgSet[m.ID.String()] = struct{}{}
		}

		filtered := make([]*model.Organization, 0)
		seen := make(map[string]struct{})

		for {
			selectedOrgs, err := h.store.Organization().List(ctx, *listParams)
			if err != nil {
				status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
				return nil, status
			}

			for _, selectedOrg := range selectedOrgs {
				if _, ok := userOrgSet[selectedOrg.ID.String()]; ok {

					// add to filtered if not already seen
					if _, already := seen[selectedOrg.ID.String()]; !already {
						filtered = append(filtered, selectedOrg)
						seen[selectedOrg.ID.String()] = struct{}{}
					}
				}
			}

			// we have limit+1 authorized items; can compute continue; stop paging
			if len(filtered) > listParams.Limit {
				break
			}

			// store returned ≤ limit raw items; no more data; stop paging
			if !(listParams.Limit > 0 && len(selectedOrgs) > listParams.Limit) {
				break
			}

			// still paging; update continue
			lastSelected := selectedOrgs[len(selectedOrgs)-1]
			nextStr := store.BuildContinueString([]string{lastSelected.ID.String()}, 1)
			nextParsed, err := store.ParseContinueString(nextStr)
			if err != nil {
				status := StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
				return nil, status
			}
			listParams.Continue = nextParsed
		}

		orgs = filtered
	}

	status = StoreErrorToApiStatus(err, false, api.OrganizationKind, nil)
	if err != nil {
		return nil, status
	}
	listMeta := api.ListMeta{}
	if listParams.Limit > 0 && len(orgs) > listParams.Limit {
		last := orgs[len(orgs)-1]
		next := store.BuildContinueString([]string{last.ID.String()}, 1)
		listMeta.Continue = next
		orgs = orgs[:listParams.Limit]
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
		Metadata:   listMeta,
	}, status
}

func (h *ServiceHandler) listUserOrganizations(ctx context.Context) ([]*model.Organization, error) {
	identity, err := common.GetIdentity(ctx)
	if err != nil {
		return nil, err
	}

	orgs, err := h.orgResolver.GetUserOrganizations(ctx, identity)
	if err != nil {
		return nil, err
	}

	return orgs, nil
}
