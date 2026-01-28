package authz

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticAuthZ_CheckPermission(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	tests := []struct {
		name     string
		roles    []string
		resource string
		op       string
		expected bool
	}{
		{
			name:     "admin user can do anything",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "admin user can delete",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "fleets",
			op:       "delete",
			expected: true,
		},
		{
			name:     "admin user can access all resources",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "organizations",
			op:       "create",
			expected: true,
		},
		{
			name:     "admin user can access enrollmentrequests",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "enrollmentrequests",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can create devices",
			roles:    []string{v1beta1.RoleOperator},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "operator can delete fleets",
			roles:    []string{v1beta1.RoleOperator},
			resource: "fleets",
			op:       "delete",
			expected: true,
		},
		{
			name:     "operator can list any resource",
			roles:    []string{v1beta1.RoleOperator},
			resource: "events",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can get any resource",
			roles:    []string{v1beta1.RoleOperator},
			resource: "repositories",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer can list any resource",
			roles:    []string{v1beta1.RoleViewer},
			resource: "devices",
			op:       "list",
			expected: true,
		},
		{
			name:     "viewer can get any resource",
			roles:    []string{v1beta1.RoleViewer},
			resource: "fleets",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer cannot create",
			roles:    []string{v1beta1.RoleViewer},
			resource: "devices",
			op:       "create",
			expected: false,
		},
		{
			name:     "viewer cannot delete",
			roles:    []string{v1beta1.RoleViewer},
			resource: "fleets",
			op:       "delete",
			expected: false,
		},
		{
			name:     "operator can create repositories",
			roles:    []string{v1beta1.RoleOperator},
			resource: "repositories",
			op:       "create",
			expected: true,
		},
		{
			name:     "operator can create imagebuilds",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imagebuilds",
			op:       "create",
			expected: true,
		},
		{
			name:     "operator can cancel imagebuilds",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imagebuilds/cancel",
			op:       "update",
			expected: true,
		},
		{
			name:     "operator can download imageexports",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imageexports/download",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer can list imagebuilds",
			roles:    []string{v1beta1.RoleViewer},
			resource: "imagebuilds",
			op:       "list",
			expected: true,
		},
		{
			name:     "viewer cannot download imageexports",
			roles:    []string{v1beta1.RoleViewer},
			resource: "imageexports/download",
			op:       "get",
			expected: false,
		},
		{
			name:     "installer can list imagebuilds",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "imagebuilds",
			op:       "list",
			expected: true,
		},
		{
			name:     "installer can download imageexports",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "imageexports/download",
			op:       "get",
			expected: true,
		},
		{
			name:     "installer cannot create imagebuilds",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "imagebuilds",
			op:       "create",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create an organization with ID
			orgID := uuid.New()
			testOrg := &model.Organization{
				ID:          orgID,
				ExternalID:  "test-org",
				DisplayName: "Test Organization",
			}

			// Create mapped identity with roles using NewMappedIdentity
			// Pass roles mapped to the organization ID
			orgRoles := map[string][]string{orgID.String(): tt.roles}
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{testOrg}, orgRoles, false, nil)

			// Create context with mapped identity and organization ID
			ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
			ctx = util.WithOrganizationID(ctx, orgID)

			// Test permission check
			allowed, err := authZ.CheckPermission(ctx, tt.resource, tt.op)

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}

func TestStaticAuthZ_GetUserPermissions(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	tests := []struct {
		name                string
		roles               []string
		expectedPermissions []v1beta1.Permission
	}{
		{
			name:  "admin user has all permissions",
			roles: []string{v1beta1.RoleAdmin},
			expectedPermissions: []v1beta1.Permission{
				{
					Resource:   "*",
					Operations: []string{"*"},
				},
			},
		},
		{
			name:  "operator user has specific permissions",
			roles: []string{v1beta1.RoleOperator},
			expectedPermissions: []v1beta1.Permission{
				{
					Resource:   "*",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "devices",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "fleets",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imagebuilds",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imagebuilds/cancel",
					Operations: []string{"update"},
				},
				{
					Resource:   "imageexports",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imageexports/cancel",
					Operations: []string{"update"},
				},
				{
					Resource:   "imageexports/download",
					Operations: []string{"get"},
				},
				{
					Resource:   "repositories",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "resourcesyncs",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
			},
		},
		{
			name:  "viewer user has read-only permissions",
			roles: []string{v1beta1.RoleViewer},
			expectedPermissions: []v1beta1.Permission{
				{
					Resource:   "*",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "imageexports/download",
					Operations: []string{}, // Explicitly denied
				},
			},
		},
		{
			name:  "installer user has limited read permissions",
			roles: []string{v1beta1.RoleInstaller},
			expectedPermissions: []v1beta1.Permission{
				{
					Resource:   "certificatesigningrequests",
					Operations: []string{"create", "get", "list", "update"},
				},
				{
					Resource:   "enrollmentrequests",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "enrollmentrequests/approval",
					Operations: []string{"update"},
				},
				{
					Resource:   "imagebuilds",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "imageexports",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "imageexports/download",
					Operations: []string{"get"},
				},
				{
					Resource:   "organizations",
					Operations: []string{"get", "list"},
				},
			},
		},
		{
			name:  "user with multiple roles has merged permissions",
			roles: []string{v1beta1.RoleViewer, v1beta1.RoleInstaller},
			expectedPermissions: []v1beta1.Permission{
				{
					Resource:   "*",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "certificatesigningrequests",
					Operations: []string{"create", "get", "list", "update"},
				},
				{
					Resource:   "enrollmentrequests",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "enrollmentrequests/approval",
					Operations: []string{"update"},
				},
				{
					Resource:   "imagebuilds",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "imageexports",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "imageexports/download",
					Operations: []string{"get"}, // Installer grants access, overriding viewer's empty list
				},
				{
					Resource:   "organizations",
					Operations: []string{"get", "list"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create an organization with ID
			orgID := uuid.New()
			testOrg := &model.Organization{
				ID:          orgID,
				ExternalID:  "test-org",
				DisplayName: "Test Organization",
			}

			// Create mapped identity with roles using NewMappedIdentity
			// Pass roles mapped to the organization ID
			orgRoles := map[string][]string{orgID.String(): tt.roles}
			mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{testOrg}, orgRoles, false, nil)

			// Create context with mapped identity and organization ID
			ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
			ctx = util.WithOrganizationID(ctx, orgID)

			// Test get user permissions
			permissionList, err := authZ.GetUserPermissions(ctx)

			require.NoError(t, err)
			require.NotNil(t, permissionList)
			assert.Equal(t, len(tt.expectedPermissions), len(permissionList.Permissions))

			// Check that all expected permissions are present (order may vary due to map iteration)
			for _, expected := range tt.expectedPermissions {
				found := false
				for _, actual := range permissionList.Permissions {
					if actual.Resource == expected.Resource {
						found = true
						assert.ElementsMatch(t, expected.Operations, actual.Operations,
							"operations mismatch for resource %s", expected.Resource)
						break
					}
				}
				assert.True(t, found, "expected permission for resource %s not found", expected.Resource)
			}
		})
	}
}

func TestStaticAuthZ_GetUserPermissions_NoIdentity(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	// Create context without mapped identity
	ctx := context.Background()

	// Test get user permissions
	permissionList, err := authZ.GetUserPermissions(ctx)

	assert.Error(t, err)
	assert.Nil(t, permissionList)
	assert.Contains(t, err.Error(), "no mapped identity found in context")
}

func TestStaticAuthZ_GetUserPermissions_NoOrgContext(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	// Create organization with ID
	orgID := uuid.New()
	testOrg := &model.Organization{
		ID:          orgID,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}

	// Create mapped identity with roles
	orgRoles := map[string][]string{orgID.String(): {v1beta1.RoleViewer}}
	mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{testOrg}, orgRoles, false, nil)

	// Create context with mapped identity but WITHOUT organization ID
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)

	// Test get user permissions
	permissionList, err := authZ.GetUserPermissions(ctx)

	assert.Error(t, err)
	assert.Nil(t, permissionList)
	assert.Contains(t, err.Error(), "no organization ID found in context")
}

func TestStaticAuthZ_GetUserPermissions_NoRolesInOrg(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	// Create organization with ID
	orgID := uuid.New()
	testOrg := &model.Organization{
		ID:          orgID,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}

	// Create mapped identity with NO roles for this organization
	orgRoles := map[string][]string{} // Empty roles
	mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{testOrg}, orgRoles, false, nil)

	// Create context with mapped identity and organization ID
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
	ctx = util.WithOrganizationID(ctx, orgID)

	// Test get user permissions
	permissionList, err := authZ.GetUserPermissions(ctx)

	require.NoError(t, err)
	require.NotNil(t, permissionList)
	assert.Equal(t, 0, len(permissionList.Permissions))
}

func TestStaticAuthZ_GetUserPermissions_SuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	// Create organization with ID
	orgID := uuid.New()
	testOrg := &model.Organization{
		ID:          orgID,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}

	// Create mapped identity as super admin
	orgRoles := map[string][]string{} // Super admins don't need org-specific roles
	mappedIdentity := identity.NewMappedIdentity("admin", "admin", []*model.Organization{testOrg}, orgRoles, true, nil)

	// Create context with mapped identity
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
	ctx = util.WithOrganizationID(ctx, orgID)

	// Test get user permissions
	permissionList, err := authZ.GetUserPermissions(ctx)

	require.NoError(t, err)
	require.NotNil(t, permissionList)
	assert.Equal(t, 1, len(permissionList.Permissions))
	assert.Equal(t, "*", permissionList.Permissions[0].Resource)
	assert.Equal(t, []string{"*"}, permissionList.Permissions[0].Operations)
}
