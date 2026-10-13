package middleware

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransformRoleNames covers the three branches of transformRoleNames:
// external roles are mapped to their internal equivalent, role names that are
// already internal are dropped, and any other role is passed through unchanged.
//
// Dropping already-internal names is deliberate: it keeps an identity provider
// from granting privileges by reporting a group literally named "admin" or
// "org-admin". Internal roles may only be obtained through the prefixed
// external role names.
func TestTransformRoleNames(t *testing.T) {
	tests := []struct {
		name     string
		roles    []string
		expected []string
	}{
		{
			name:     "When the external admin role is given it should map to the internal admin role",
			roles:    []string{v1beta1.ExternalRoleAdmin},
			expected: []string{v1beta1.RoleAdmin},
		},
		{
			name:     "When the external org-admin role is given it should map to the internal org-admin role",
			roles:    []string{v1beta1.ExternalRoleOrgAdmin},
			expected: []string{v1beta1.RoleOrgAdmin},
		},
		{
			name:     "When the external operator role is given it should map to the internal operator role",
			roles:    []string{v1beta1.ExternalRoleOperator},
			expected: []string{v1beta1.RoleOperator},
		},
		{
			name:     "When the external viewer role is given it should map to the internal viewer role",
			roles:    []string{v1beta1.ExternalRoleViewer},
			expected: []string{v1beta1.RoleViewer},
		},
		{
			name:     "When the external installer role is given it should map to the internal installer role",
			roles:    []string{v1beta1.ExternalRoleInstaller},
			expected: []string{v1beta1.RoleInstaller},
		},
		{
			name: "When every external role is given it should map each one in order",
			roles: []string{
				v1beta1.ExternalRoleAdmin,
				v1beta1.ExternalRoleOrgAdmin,
				v1beta1.ExternalRoleOperator,
				v1beta1.ExternalRoleViewer,
				v1beta1.ExternalRoleInstaller,
			},
			expected: []string{
				v1beta1.RoleAdmin,
				v1beta1.RoleOrgAdmin,
				v1beta1.RoleOperator,
				v1beta1.RoleViewer,
				v1beta1.RoleInstaller,
			},
		},
		{
			name:     "When a role name matches no known role it should be kept unchanged",
			roles:    []string{"some-idp-group"},
			expected: []string{"some-idp-group"},
		},
		{
			name:     "When several unknown role names are given it should keep all of them in order",
			roles:    []string{"group-b", "group-a"},
			expected: []string{"group-b", "group-a"},
		},
		{
			name:     "When a role name only resembles an external role it should be kept unchanged",
			roles:    []string{"flightctl-superuser"},
			expected: []string{"flightctl-superuser"},
		},
		{
			name:     "When an already-internal role name is given it should be dropped",
			roles:    []string{v1beta1.RoleAdmin},
			expected: []string{},
		},
		{
			name: "When every internal role name is given it should drop all of them",
			roles: []string{
				v1beta1.RoleAdmin,
				v1beta1.RoleOrgAdmin,
				v1beta1.RoleOperator,
				v1beta1.RoleViewer,
				v1beta1.RoleInstaller,
			},
			expected: []string{},
		},
		{
			name:     "When external and internal names of the same role are given it should keep only the mapped one",
			roles:    []string{v1beta1.ExternalRoleOrgAdmin, v1beta1.RoleOrgAdmin},
			expected: []string{v1beta1.RoleOrgAdmin},
		},
		{
			name:     "When external, internal and unknown roles are mixed it should map, drop and keep them respectively",
			roles:    []string{v1beta1.ExternalRoleOperator, v1beta1.RoleAdmin, "some-idp-group", v1beta1.ExternalRoleViewer},
			expected: []string{v1beta1.RoleOperator, "some-idp-group", v1beta1.RoleViewer},
		},
		{
			name:     "When the same external role is given twice it should map both occurrences",
			roles:    []string{v1beta1.ExternalRoleViewer, v1beta1.ExternalRoleViewer},
			expected: []string{v1beta1.RoleViewer, v1beta1.RoleViewer},
		},
		{
			name:     "When the role list is empty it should return an empty list",
			roles:    []string{},
			expected: []string{},
		},
		{
			name:     "When the role list is nil it should return nil",
			roles:    nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, transformRoleNames(tt.roles))
		})
	}
}

// TestTransformRoleNamesMapsEveryKnownExternalRole guards against a new entry
// being added to KnownExternalRoles without a matching entry in roleNameMap,
// which would silently pass the new role through untransformed.
func TestTransformRoleNamesMapsEveryKnownExternalRole(t *testing.T) {
	require.NotEmpty(t, v1beta1.KnownExternalRoles)

	for _, external := range v1beta1.KnownExternalRoles {
		t.Run("When the external role "+external+" is transformed it should produce an internal role", func(t *testing.T) {
			transformed := transformRoleNames([]string{external})

			require.Len(t, transformed, 1, "external role %q was not mapped to exactly one role", external)
			assert.NotEqual(t, external, transformed[0], "external role %q was passed through instead of mapped", external)
			assert.True(t, internalRoles[transformed[0]],
				"external role %q mapped to %q, which is not an internal role", external, transformed[0])
		})
	}
}

// TestTransformRoleNamesDoesNotMutateInput verifies the caller's slice is left
// untouched, since the result is stored separately from the reported roles.
func TestTransformRoleNamesDoesNotMutateInput(t *testing.T) {
	roles := []string{v1beta1.ExternalRoleOrgAdmin, v1beta1.RoleAdmin, "some-idp-group"}
	original := make([]string, len(roles))
	copy(original, roles)

	transformRoleNames(roles)

	assert.Equal(t, original, roles)
}
