package aap

import (
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
func (a *AAPGatewayClient) ListUserTeams(token string, userID string) ([]*AAPTeam, error) {
	path := a.appendQueryParams(fmt.Sprintf("/api/gateway/v1/users/%s/teams", userID))
	return getWithPagination[AAPTeam](a, path, token)
}
