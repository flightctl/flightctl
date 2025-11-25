package aap

import (
	"context"
	"fmt"
)

type AAPUser struct {
	ID                int    `json:"id,omitempty"`
	Username          string `json:"username,omitempty"`
	IsSuperuser       bool   `json:"is_superuser,omitempty"`
	IsPlatformAuditor bool   `json:"is_platform_auditor,omitempty"`
}

type AAPUsersResponse = AAPPaginatedResponse[AAPUser]

// GET /api/gateway/v1/me/
func (a *AAPGatewayClient) GetMe(ctx context.Context, token string) (*AAPUser, error) {
	endpoint := a.buildEndpoint("/api/gateway/v1/me/", nil)

	result, err := getWithPagination[AAPUser](a, ctx, endpoint, token)
	if err != nil {
		return nil, err
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no user info in /me response")
	}

	if len(result) > 1 {
		return nil, fmt.Errorf("multiple users /me response")
	}

	return result[0], nil
}
