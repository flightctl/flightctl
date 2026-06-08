package authn

import (
	"testing"

	"github.com/flightctl/flightctl/pkg/aap"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func TestMapRoleAssignmentsToOrgRoles_WithPrefix(t *testing.T) {
	tests := []struct {
		name            string
		roleAssignments []*aap.AAPRoleUserAssignment
		prefix          *string
		expected        map[string][]string
	}{
		{
			name:            "nil prefix returns unprefixed org names",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          nil,
			expected:        map[string][]string{"OrgA": {"admin"}},
		},
		{
			name:            "empty prefix returns unprefixed org names",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          lo.ToPtr(""),
			expected:        map[string][]string{"OrgA": {"admin"}},
		},
		{
			name:            "prefix is prepended to org name",
			roleAssignments: createUserRoleAssignments("OrgA", "admin"),
			prefix:          lo.ToPtr("aap-"),
			expected:        map[string][]string{"aap-OrgA": {"admin"}},
		},
		{
			name: "prefix applied to multiple orgs",
			roleAssignments: append(
				createUserRoleAssignments("OrgA", "admin"),
				createUserRoleAssignments("OrgB", "viewer")...,
			),
			prefix: lo.ToPtr("aap-"),
			expected: map[string][]string{
				"aap-OrgA": {"admin"},
				"aap-OrgB": {"viewer"},
			},
		},
		{
			name: "multiple roles for same org with prefix",
			roleAssignments: append(
				createUserRoleAssignments("OrgA", "admin"),
				createUserRoleAssignments("OrgA", "viewer")...,
			),
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{"aap-OrgA": {"admin", "viewer"}},
		},
		{
			name: "non-organization content types are ignored",
			roleAssignments: []*aap.AAPRoleUserAssignment{
				{
					ContentType: "shared.team",
					SummaryFields: aap.AAPRoleUserAssignmentSummaryFields{
						ContentObject:  aap.AAPContentObject{Name: "SomeTeam"},
						RoleDefinition: aap.AAPRoleDefinition{Name: "admin"},
					},
				},
			},
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{},
		},
	}

	auth := &AapGatewayAuth{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := auth.mapRoleAssignmentsToOrgRoles(tt.roleAssignments, tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestMapTeamRoleAssignmentsToOrgRoles_WithPrefix(t *testing.T) {
	tests := []struct {
		name            string
		roleAssignments []*aap.AAPRoleTeamAssignment
		prefix          *string
		expected        map[string][]string
	}{
		{
			name:            "nil prefix returns unprefixed org names",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          nil,
			expected:        map[string][]string{"OrgA": {"operator"}},
		},
		{
			name:            "empty prefix returns unprefixed org names",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          lo.ToPtr(""),
			expected:        map[string][]string{"OrgA": {"operator"}},
		},
		{
			name:            "prefix is prepended to org name",
			roleAssignments: createTeamRoleAssignments("OrgA", "operator"),
			prefix:          lo.ToPtr("aap-"),
			expected:        map[string][]string{"aap-OrgA": {"operator"}},
		},
		{
			name: "prefix applied to multiple orgs from teams",
			roleAssignments: append(
				createTeamRoleAssignments("OrgA", "operator"),
				createTeamRoleAssignments("OrgB", "viewer")...,
			),
			prefix: lo.ToPtr("aap-"),
			expected: map[string][]string{
				"aap-OrgA": {"operator"},
				"aap-OrgB": {"viewer"},
			},
		},
		{
			name: "duplicate roles are not added",
			roleAssignments: append(
				createTeamRoleAssignments("OrgA", "viewer"),
				createTeamRoleAssignments("OrgA", "viewer")...,
			),
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{"aap-OrgA": {"viewer"}},
		},
		{
			name: "non-organization content types are ignored",
			roleAssignments: []*aap.AAPRoleTeamAssignment{
				{
					ContentType: "shared.team",
					SummaryFields: aap.AAPRoleTeamAssignmentSummaryFields{
						ContentObject:  aap.AAPContentObject{Name: "SomeTeam"},
						RoleDefinition: aap.AAPRoleDefinition{Name: "admin"},
					},
				},
			},
			prefix:   lo.ToPtr("aap-"),
			expected: map[string][]string{},
		},
	}

	auth := &AapGatewayAuth{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := auth.mapTeamRoleAssignmentsToOrgRoles(tt.roleAssignments, tt.prefix)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func createUserRoleAssignments(orgName, roleName string) []*aap.AAPRoleUserAssignment {
	return []*aap.AAPRoleUserAssignment{
		{
			ContentType: "shared.organization",
			SummaryFields: aap.AAPRoleUserAssignmentSummaryFields{
				ContentObject:  aap.AAPContentObject{Name: orgName},
				RoleDefinition: aap.AAPRoleDefinition{Name: roleName},
			},
		},
	}
}

func createTeamRoleAssignments(orgName, roleName string) []*aap.AAPRoleTeamAssignment {
	return []*aap.AAPRoleTeamAssignment{
		{
			ContentType: "shared.organization",
			SummaryFields: aap.AAPRoleTeamAssignmentSummaryFields{
				ContentObject:  aap.AAPContentObject{Name: orgName},
				RoleDefinition: aap.AAPRoleDefinition{Name: roleName},
			},
		},
	}
}
