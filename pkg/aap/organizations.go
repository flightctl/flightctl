package aap

import (
	"context"
	"fmt"
)

type AAPOrganization struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type AAPOrganizationsResponse = AAPPaginatedResponse[AAPOrganization]

// GET /api/gateway/v1/organizations/{organization_id}
func (a *AAPGatewayClient) GetOrganization(ctx context.Context, token string, organizationID string) (*AAPOrganization, error) {
	path := a.appendQueryParams(fmt.Sprintf("/api/gateway/v1/organizations/%s", organizationID))
	result, err := get[AAPOrganization](a, ctx, path, token)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GET /api/gateway/v1/organizations
func (a *AAPGatewayClient) ListOrganizations(ctx context.Context, token string) ([]*AAPOrganization, error) {
	path := a.appendQueryParams("/api/gateway/v1/organizations")
	return getWithPagination[AAPOrganization](a, ctx, path, token)
}

// GET /api/gateway/v1/users/{user_id}/organizations
func (a *AAPGatewayClient) ListUserOrganizations(ctx context.Context, token string, userID string) ([]*AAPOrganization, error) {
	path := a.appendQueryParams(fmt.Sprintf("/api/gateway/v1/users/%s/organizations", userID))
	return getWithPagination[AAPOrganization](a, ctx, path, token)
}
