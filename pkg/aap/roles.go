package aap

import (
	"context"
	"fmt"
	"net/url"
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

// GET /api/controller/v2/role_user_assignments/?user__id={user_id}
func (a *AAPGatewayClient) ListUserRoleAssignments(ctx context.Context, token string, userID string) ([]*AAPRoleUserAssignment, error) {
	// Build query parameters using url.Values
	query := url.Values{}
	query.Set("user__id", userID)
	if a.maxPageSize != nil {
		query.Set("page_size", fmt.Sprintf("%d", *a.maxPageSize))
	}

	endpoint := a.buildEndpoint("/api/controller/v2/role_user_assignments/", query)
	return getWithPagination[AAPRoleUserAssignment](a, ctx, endpoint, token)
}
