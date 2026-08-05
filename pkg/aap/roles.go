package aap

import (
	"context"
	"net/url"
	"strconv"
)

// RoleDefinition represents an AAP role definition
type AAPRoleDefinition struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Managed     bool   `json:"managed"`
}

// User represents an AAP user in role assignments
type AAPRoleUser struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

// ContentObject represents the organization in role assignments
type AAPContentObject struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// RoleUserAssignmentSummaryFields contains nested summary information
type AAPRoleUserAssignmentSummaryFields struct {
	RoleDefinition AAPRoleDefinition `json:"role_definition"`
	User           AAPRoleUser       `json:"user"`
	ContentObject  AAPContentObject  `json:"content_object"`
}

// RoleUserAssignment represents a user's role assignment to an organization
type AAPRoleUserAssignment struct {
	ID             int                                `json:"id"`
	SummaryFields  AAPRoleUserAssignmentSummaryFields `json:"summary_fields"`
	ContentType    string                             `json:"content_type"`
	ObjectID       string                             `json:"object_id"`
	RoleDefinition int                                `json:"role_definition"`
	User           int                                `json:"user"`
}

type AAPRoleUserAssignmentsResponse = AAPPaginatedResponse[AAPRoleUserAssignment]

// AAPTeamSummary represents a team in role assignment summary fields
type AAPTeamSummary struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// AAPRoleTeamAssignmentSummaryFields contains nested summary information for team role assignments
type AAPRoleTeamAssignmentSummaryFields struct {
	RoleDefinition AAPRoleDefinition `json:"role_definition"`
	Team           AAPTeamSummary    `json:"team"`
	ContentObject  AAPContentObject  `json:"content_object"`
}

// AAPRoleTeamAssignment represents a team's role assignment to an organization
type AAPRoleTeamAssignment struct {
	ID             int                                `json:"id"`
	SummaryFields  AAPRoleTeamAssignmentSummaryFields `json:"summary_fields"`
	ContentType    string                             `json:"content_type"`
	ObjectID       string                             `json:"object_id"`
	RoleDefinition int                                `json:"role_definition"`
	Team           int                                `json:"team"`
}

// GET /api/controller/v2/role_user_assignments/?user__resource__ansible_id={ansible_id}
//
// Filters by ansible_id rather than the Gateway numeric user ID: a user's numeric ID is not
// guaranteed to be the same between the Gateway and Controller components, but ansible_id is.
// Custom role definitions (e.g. flightctl-org-admin) are only visible through the Controller
// API today, not the Gateway's role_user_assignments endpoint.
func (a *AAPGatewayClient) ListUserRoleAssignments(ctx context.Context, token string, ansibleID string) ([]*AAPRoleUserAssignment, error) {
	query := url.Values{}
	query.Set("user__resource__ansible_id", ansibleID)
	if a.maxPageSize != nil {
		query.Set("page_size", strconv.Itoa(*a.maxPageSize))
	}

	endpoint := a.buildEndpoint("/api/controller/v2/role_user_assignments/", query)
	return getWithPagination[AAPRoleUserAssignment](a, ctx, endpoint, token)
}

// GET /api/controller/v2/role_team_assignments/?team__resource__ansible_id={ansible_id}
//
// See ListUserRoleAssignments for why this filters by ansible_id and uses the Controller API.
func (a *AAPGatewayClient) ListTeamRoleAssignments(ctx context.Context, token string, ansibleID string) ([]*AAPRoleTeamAssignment, error) {
	query := url.Values{}
	query.Set("team__resource__ansible_id", ansibleID)
	if a.maxPageSize != nil {
		query.Set("page_size", strconv.Itoa(*a.maxPageSize))
	}

	endpoint := a.buildEndpoint("/api/controller/v2/role_team_assignments/", query)
	return getWithPagination[AAPRoleTeamAssignment](a, ctx, endpoint, token)
}
