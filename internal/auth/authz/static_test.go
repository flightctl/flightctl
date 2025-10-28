package authz

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/stretchr/testify/assert"
)

func TestStaticAuthZ_CheckPermission(t *testing.T) {
	authZ := NewStaticAuthZ()

	tests := []struct {
		name     string
		roles    []string
		resource string
		op       string
		expected bool
	}{
		{
			name:     "admin user can do anything",
			roles:    []string{v1alpha1.RoleAdmin},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "admin user can delete",
			roles:    []string{v1alpha1.RoleAdmin},
			resource: "fleets",
			op:       "delete",
			expected: true,
		},
		{
			name:     "admin user can access all resources",
			roles:    []string{v1alpha1.RoleAdmin},
			resource: "organizations",
			op:       "create",
			expected: true,
		},
		{
			name:     "admin user can access enrollmentrequests",
			roles:    []string{v1alpha1.RoleAdmin},
			resource: "enrollmentrequests",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can create devices",
			roles:    []string{v1alpha1.RoleOperator},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "operator can delete fleets",
			roles:    []string{v1alpha1.RoleOperator},
			resource: "fleets",
			op:       "delete",
			expected: true,
		},
		{
			name:     "operator can list any resource",
			roles:    []string{v1alpha1.RoleOperator},
			resource: "events",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can get any resource",
			roles:    []string{v1alpha1.RoleOperator},
			resource: "repositories",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer can list any resource",
			roles:    []string{v1alpha1.RoleViewer},
			resource: "devices",
			op:       "list",
			expected: true,
		},
		{
			name:     "viewer can get any resource",
			roles:    []string{v1alpha1.RoleViewer},
			resource: "fleets",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer cannot create",
			roles:    []string{v1alpha1.RoleViewer},
			resource: "devices",
			op:       "create",
			expected: false,
		},
		{
			name:     "viewer cannot delete",
			roles:    []string{v1alpha1.RoleViewer},
			resource: "fleets",
			op:       "delete",
			expected: false,
		},
		{
			name:     "operator cannot create repositories",
			roles:    []string{v1alpha1.RoleOperator},
			resource: "repositories",
			op:       "create",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mapped identity with roles using NewMappedIdentity
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{}, tt.roles, nil)

			// Create context with mapped identity
			ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)

			// Test permission check
			allowed, err := authZ.CheckPermission(ctx, tt.resource, tt.op)

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}
