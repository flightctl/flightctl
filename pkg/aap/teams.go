package aap

import (
	"context"
	"fmt"
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
	path := a.appendQueryParams(fmt.Sprintf("/api/gateway/v1/users/%s/teams", userID))
	return getWithPagination[AAPTeam](a, ctx, path, token)
}
