package service

import (
	"context"
	"fmt"
	"net/http"
	"regexp"

	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", domain.APIGroup, domain.OrganizationAPIVersion)

// organizationModelToAPI converts a model.Organization to domain.Organization
func organizationModelToAPI(org *model.Organization) domain.Organization {
	name := org.ID.String()
	return domain.Organization{
		ApiVersion: organizationApiVersion,
		Kind:       domain.OrganizationKind,
		Metadata:   domain.ObjectMeta{Name: &name},
		Spec: &domain.OrganizationSpec{
			ExternalId:  &org.ExternalID,
			DisplayName: &org.DisplayName,
		},
	}
}

func (h *ServiceHandler) ListOrganizations(ctx context.Context, params domain.ListOrganizationsParams) (*domain.OrganizationList, domain.Status) {
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
			status := StoreErrorToApiStatus(listErr, false, domain.OrganizationKind, nil)
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
			status := StoreErrorToApiStatus(err, false, domain.OrganizationKind, nil)
			return nil, status
		}
	}

	apiOrgs := make([]domain.Organization, len(orgs))
	for i, org := range orgs {
		apiOrgs[i] = organizationModelToAPI(org)
	}

	return &domain.OrganizationList{
		Items:      apiOrgs,
		ApiVersion: organizationApiVersion,
		Kind:       domain.OrganizationListKind,
		Metadata:   domain.ListMeta{},
	}, domain.StatusOK()
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
