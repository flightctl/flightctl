package aap

import (
	"context"
	"fmt"
	"net/url"
)

// DefaultOrganizationID is the org ID reserved for the default / system organization within AAP
const DefaultOrganizationID = 1

type AAPOrganization struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type AAPOrganizationsResponse = AAPPaginatedResponse[AAPOrganization]

// GET /api/gateway/v1/organizations/{organization_id}
func (a *AAPGatewayClient) GetOrganization(ctx context.Context, token string, organizationID string) (*AAPOrganization, error) {
	path := fmt.Sprintf("/api/gateway/v1/organizations/%s", organizationID)

	var query url.Values
	if a.maxPageSize != nil {
		query = url.Values{}
		query.Set("page_size", fmt.Sprintf("%d", *a.maxPageSize))
	}

	endpoint := a.buildEndpoint(path, query)
	result, err := get[AAPOrganization](a, ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GET /api/gateway/v1/organizations
func (a *AAPGatewayClient) ListOrganizations(ctx context.Context, token string) ([]*AAPOrganization, error) {
	var query url.Values
	if a.maxPageSize != nil {
		query = url.Values{}
		query.Set("page_size", fmt.Sprintf("%d", *a.maxPageSize))
	}

	endpoint := a.buildEndpoint("/api/gateway/v1/organizations", query)
	return getWithPagination[AAPOrganization](a, ctx, endpoint, token)
}

// GET /api/gateway/v1/users/{user_id}/organizations
func (a *AAPGatewayClient) ListUserOrganizations(ctx context.Context, token string, userID string) ([]*AAPOrganization, error) {
	path := fmt.Sprintf("/api/gateway/v1/users/%s/organizations", userID)

	var query url.Values
	if a.maxPageSize != nil {
		query = url.Values{}
		query.Set("page_size", fmt.Sprintf("%d", *a.maxPageSize))
	}

	endpoint := a.buildEndpoint(path, query)
	return getWithPagination[AAPOrganization](a, ctx, endpoint, token)
}
