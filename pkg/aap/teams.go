package aap

import (
	"context"
	"fmt"
	"net/url"
)

type AAPTeamSummaryFields struct {
	Organization AAPOrganization `json:"organization"`
}

type AAPTeam struct {
	ID            int                  `json:"id"`
	SummaryFields AAPTeamSummaryFields `json:"summary_fields"`
}

type AAPTeamsResponse = AAPPaginatedResponse[AAPTeam]

// GET /api/gateway/v1/users/{user_id}/teams
func (a *AAPGatewayClient) ListUserTeams(ctx context.Context, token string, userID string) ([]*AAPTeam, error) {
	path := fmt.Sprintf("/api/gateway/v1/users/%s/teams", userID)

	var query url.Values
	if a.maxPageSize != nil {
		query = url.Values{}
		query.Set("page_size", fmt.Sprintf("%d", *a.maxPageSize))
	}

	endpoint := a.buildEndpoint(path, query)
	return getWithPagination[AAPTeam](a, ctx, endpoint, token)
}
