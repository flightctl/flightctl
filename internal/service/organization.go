package service

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
)

var organizationApiVersion = fmt.Sprintf("%s/%s", api.APIGroup, api.OrganizationAPIVersion)

func (h *ServiceHandler) ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	internalRequest, ok := util.IsInternalRequest(ctx)
	if !ok {
		h.log.Warn("unsupported internal request context found")
	}

	var orgs []*model.Organization
	var err error
	if internalRequest {
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
