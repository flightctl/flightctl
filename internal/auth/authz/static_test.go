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
			name:     "admin user can create catalog",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "catalogs",
			op:       "create",
			expected: true,
		},
		{
			name:     "admin user can edit catalogitems",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "catalogitems",
			op:       "update",
			expected: true,
		},
		{
			name:     "org admin can revoke enrollment credentials",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "certificatesigningrequests/revocation",
			op:       "create",
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
			name:     "operator can list catalog",
			roles:    []string{v1beta1.RoleOperator},
			resource: "catalogs",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can list catalogitems",
			roles:    []string{v1beta1.RoleOperator},
			resource: "catalogitems",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator cannot delete catalog",
			roles:    []string{v1beta1.RoleOperator},
			resource: "catalogs",
			op:       "delete",
			expected: false,
		},
		{
			name:     "operator cannot delete catalogitems",
			roles:    []string{v1beta1.RoleOperator},
			resource: "catalogitems",
			op:       "delete",
			expected: false,
		},
		{
			name:     "operator cannot revoke enrollment credentials",
			roles:    []string{v1beta1.RoleOperator},
			resource: "certificatesigningrequests/revocation",
			op:       "create",
			expected: false,
		},
		{
			name:     "operator can list imagepromotions",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imagepromotions",
			op:       "list",
			expected: true,
		},
		{
			name:     "operator can delete imagepromotions",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imagepromotions",
			op:       "delete",
			expected: true,
		},
		{
			name:     "operator can create imagepromotions",
			roles:    []string{v1beta1.RoleOperator},
			resource: "imagepromotions",
			op:       "create",
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
			name:     "viewer can get any resource",
			roles:    []string{v1beta1.RoleViewer},
			resource: "repositories",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer cannot access device console",
			roles:    []string{v1beta1.RoleViewer},
			resource: "devices/console",
			op:       "get",
			expected: false,
		},
		{
			name:     "operator can access device console",
			roles:    []string{v1beta1.RoleOperator},
			resource: "devices/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer cannot access devices/applications/console",
			roles:    []string{v1beta1.RoleViewer},
			resource: "devices/applications/console",
			op:       "get",
			expected: false,
		},
		{
			name:     "operator can access devices/applications/console",
			roles:    []string{v1beta1.RoleOperator},
			resource: "devices/applications/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "viewer can list catalog",
			roles:    []string{v1beta1.RoleViewer},
			resource: "catalogs",
			op:       "list",
			expected: true,
		},
		{
			name:     "viewer can list catalogitems",
			roles:    []string{v1beta1.RoleViewer},
			resource: "catalogitems",
			op:       "list",
			expected: true,
		},
		{
			name:     "viewer cannot delete catalog",
			roles:    []string{v1beta1.RoleViewer},
			resource: "catalogs",
			op:       "delete",
			expected: false,
		},
		{
			name:     "viewer can not delete catalogitems",
			roles:    []string{v1beta1.RoleViewer},
			resource: "catalogitems",
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
			op:       "create",
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
		{
			name:     "installer cannot list catalog",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "catalogs",
			op:       "list",
			expected: false,
		},
		{
			name:     "installer cannot list catalogitems",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "catalogitems",
			op:       "list",
			expected: false,
		},
		{
			name:     "installer cannot delete catalog",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "catalogs",
			op:       "delete",
			expected: false,
		},
		{
			name:     "installer can not delete catalogitems",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "catalogitems",
			op:       "delete",
			expected: false,
		},
		{
			name:     "installer cannot revoke enrollment credentials",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "certificatesigningrequests/revocation",
			op:       "create",
			expected: false,
		},
		// Enrollment hook override RBAC
		{
			name:     "When admin updates enrollment hook override it should succeed",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "devices/enrollmenthooks/override",
			op:       "update",
			expected: true,
		},
		{
			name:     "When org-admin updates enrollment hook override it should succeed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "devices/enrollmenthooks/override",
			op:       "update",
			expected: true,
		},
		{
			name:     "When operator updates enrollment hook override it should succeed",
			roles:    []string{v1beta1.RoleOperator},
			resource: "devices/enrollmenthooks/override",
			op:       "update",
			expected: true,
		},
		{
			name:     "When viewer updates enrollment hook override it should be denied",
			roles:    []string{v1beta1.RoleViewer},
			resource: "devices/enrollmenthooks/override",
			op:       "update",
			expected: false,
		},
		{
			name:     "When installer updates enrollment hook override it should be denied",
			roles:    []string{v1beta1.RoleInstaller},
			resource: "devices/enrollmenthooks/override",
			op:       "update",
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
					Resource:   "catalogitems",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "catalogs",
					Operations: []string{"get", "list"},
				},
				{
					Resource:   "devices",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "devices/applications/lifecycle",
					Operations: []string{"update"},
				},
				{
					Resource:   "devices/enrollmenthooks/override",
					Operations: []string{"update"},
				},
				{
					Resource:   "enrollmenthookpolicies",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "fleets",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "fleets/applications/lifecycle",
					Operations: []string{"update"},
				},
				{
					Resource:   "labelsyncmappings",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imagebuilds",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imagebuilds/cancel",
					Operations: []string{"create"},
				},
				{
					Resource:   "imagebuilds/newversion",
					Operations: []string{"create"},
				},
				{
					Resource:   "imageexports",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "imageexports/cancel",
					Operations: []string{"create"},
				},
				{
					Resource:   "imageexports/download",
					Operations: []string{"get"},
				},
				{
					Resource:   "imagepromotions",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "repositories",
					Operations: []string{"create", "delete", "get", "list", "patch", "update"},
				},
				{
					Resource:   "repositories/check-oci-tag",
					Operations: []string{"create"},
				},
				{
					Resource:   "repositories/check-oci-image",
					Operations: []string{"create"},
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
					Resource:   "devices/applications/console",
					Operations: []string{}, // Explicitly denied
				},
				{
					Resource:   "devices/console",
					Operations: []string{}, // Explicitly denied
				},
				{
					Resource:   "devices/enrollmenthooks/override",
					Operations: []string{}, // Explicitly denied - ManualOverride requires operator or admin role
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
					Resource:   "devices/applications/console",
					Operations: []string{}, // Explicitly denied by viewer, installer does not grant it
				},
				{
					Resource:   "devices/console",
					Operations: []string{}, // Explicitly denied by viewer, installer does not grant it
				},
				{
					Resource:   "devices/enrollmenthooks/override",
					Operations: []string{}, // Explicitly denied by viewer, installer does not grant it
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

// ctxWithOrgRoles builds a context carrying a mapped identity whose roles are
// scoped per organization, together with the organization selected for the
// request. Roles granted in one organization must not apply to another.
func ctxWithOrgRoles(t *testing.T, orgs []*model.Organization, orgRoles map[string][]string, selectedOrg uuid.UUID) context.Context {
	t.Helper()

	mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", orgs, orgRoles, false, nil)
	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
	return util.WithOrganizationID(ctx, selectedOrg)
}

// TestStaticAuthZ_CheckPermission_WildcardRoles covers the org-admin and admin
// roles, which both grant "*" on "*". In particular it pins down that they are
// not subject to the explicit denials that constrain the viewer role.
func TestStaticAuthZ_CheckPermission_WildcardRoles(t *testing.T) {
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
			name:     "When org-admin creates devices it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "When org-admin deletes fleets it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "fleets",
			op:       "delete",
			expected: true,
		},
		{
			name:     "When org-admin patches repositories it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "repositories",
			op:       "patch",
			expected: true,
		},
		{
			name:     "When org-admin opens a device console it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "devices/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "When org-admin opens an application console it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "devices/applications/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "When org-admin downloads an image export it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "imageexports/download",
			op:       "get",
			expected: true,
		},
		{
			name:     "When org-admin approves an enrollment request it should be allowed",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "enrollmentrequests/approval",
			op:       "update",
			expected: true,
		},
		{
			name:     "When org-admin acts on a resource with no specific entry it should be allowed via the wildcard",
			roles:    []string{v1beta1.RoleOrgAdmin},
			resource: "events",
			op:       "delete",
			expected: true,
		},
		{
			name:     "When admin opens a device console it should be allowed",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "devices/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "When admin downloads an image export it should be allowed",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "imageexports/download",
			op:       "get",
			expected: true,
		},
		{
			name:     "When admin acts on a resource with no specific entry it should be allowed via the wildcard",
			roles:    []string{v1beta1.RoleAdmin},
			resource: "events",
			op:       "delete",
			expected: true,
		},
		{
			name:     "When org-admin is combined with viewer it should still be allowed to create",
			roles:    []string{v1beta1.RoleViewer, v1beta1.RoleOrgAdmin},
			resource: "devices",
			op:       "create",
			expected: true,
		},
		{
			name:     "When org-admin is combined with viewer it should override the viewer console denial",
			roles:    []string{v1beta1.RoleViewer, v1beta1.RoleOrgAdmin},
			resource: "devices/console",
			op:       "get",
			expected: true,
		},
		{
			name:     "When an unrecognized role is the only role it should be denied",
			roles:    []string{"some-idp-group"},
			resource: "devices",
			op:       "get",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orgID := uuid.New()
			testOrg := &model.Organization{
				ID:          orgID,
				ExternalID:  "test-org",
				DisplayName: "Test Organization",
			}
			ctx := ctxWithOrgRoles(t, []*model.Organization{testOrg}, map[string][]string{orgID.String(): tt.roles}, orgID)

			allowed, err := authZ.CheckPermission(ctx, tt.resource, tt.op)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}

// TestStaticAuthZ_CheckPermission_RolesAreOrgScoped verifies that a role is
// only honored in the organization it was granted for. A user who is org-admin
// in one organization must not inherit those privileges in another.
func TestStaticAuthZ_CheckPermission_RolesAreOrgScoped(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	firstOrgID := uuid.New()
	secondOrgID := uuid.New()
	orgs := []*model.Organization{
		{ID: firstOrgID, ExternalID: "first-org", DisplayName: "First Organization"},
		{ID: secondOrgID, ExternalID: "second-org", DisplayName: "Second Organization"},
	}

	// Org-admin in the first organization, viewer in the second.
	orgRoles := map[string][]string{
		firstOrgID.String():  {v1beta1.RoleOrgAdmin},
		secondOrgID.String(): {v1beta1.RoleViewer},
	}

	tests := []struct {
		name        string
		selectedOrg uuid.UUID
		resource    string
		op          string
		expected    bool
	}{
		{
			name:        "When org-admin creates devices in the organization granting the role it should be allowed",
			selectedOrg: firstOrgID,
			resource:    "devices",
			op:          "create",
			expected:    true,
		},
		{
			name:        "When org-admin creates devices in another organization it should be denied",
			selectedOrg: secondOrgID,
			resource:    "devices",
			op:          "create",
			expected:    false,
		},
		{
			name:        "When org-admin opens a device console in another organization it should be denied",
			selectedOrg: secondOrgID,
			resource:    "devices/console",
			op:          "get",
			expected:    false,
		},
		{
			name:        "When the viewer role applies in the selected organization it should still allow reads",
			selectedOrg: secondOrgID,
			resource:    "devices",
			op:          "list",
			expected:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := ctxWithOrgRoles(t, orgs, orgRoles, tt.selectedOrg)

			allowed, err := authZ.CheckPermission(ctx, tt.resource, tt.op)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, allowed)
		})
	}
}

// TestStaticAuthZ_CheckPermission_RoleInUnrelatedOrgOnly verifies that holding
// a role in some organization grants nothing in an organization the user has
// no roles for.
func TestStaticAuthZ_CheckPermission_RoleInUnrelatedOrgOnly(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	grantedOrgID := uuid.New()
	selectedOrgID := uuid.New()
	orgs := []*model.Organization{
		{ID: grantedOrgID, ExternalID: "granted-org", DisplayName: "Granted Organization"},
		{ID: selectedOrgID, ExternalID: "selected-org", DisplayName: "Selected Organization"},
	}
	orgRoles := map[string][]string{grantedOrgID.String(): {v1beta1.RoleOrgAdmin}}

	ctx := ctxWithOrgRoles(t, orgs, orgRoles, selectedOrgID)

	allowed, err := authZ.CheckPermission(ctx, "devices", "get")

	require.NoError(t, err)
	assert.False(t, allowed)
}

// TestStaticAuthZ_GetUserPermissions_OrgAdmin verifies that org-admin reports
// the same full-access permission set as admin.
func TestStaticAuthZ_GetUserPermissions_OrgAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	authZ := NewStaticAuthZ(log)

	orgID := uuid.New()
	testOrg := &model.Organization{
		ID:          orgID,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}
	ctx := ctxWithOrgRoles(t, []*model.Organization{testOrg}, map[string][]string{orgID.String(): {v1beta1.RoleOrgAdmin}}, orgID)

	permissionList, err := authZ.GetUserPermissions(ctx)

	require.NoError(t, err)
	require.NotNil(t, permissionList)
	require.Len(t, permissionList.Permissions, 1)
	assert.Equal(t, "*", permissionList.Permissions[0].Resource)
	assert.Equal(t, []string{"*"}, permissionList.Permissions[0].Operations)
}

// TestExpandPermissions_WildcardRoles verifies that a role whose only entry is
// "*": {"*"} collapses to a single wildcard rule rather than being enumerated
// across every resource.
func TestExpandPermissions_WildcardRoles(t *testing.T) {
	permissions := GetResourcePermissions()
	allResources := []string{"devices", "devices/console", "imageexports/download"}

	for _, role := range []string{v1beta1.RoleOrgAdmin, v1beta1.RoleAdmin} {
		t.Run("When "+role+" permissions are expanded it should collapse to a single wildcard rule", func(t *testing.T) {
			perms := permissions[role]
			require.NotNil(t, perms, "role %q not found in resourcePermissions", role)

			expanded := ExpandPermissions(perms, allResources)

			assert.Equal(t, map[string][]string{"*": {"*"}}, expanded)
		})
	}
}

// TestExpandPermissions_RoleScopedExpansion verifies the precedence rules for
// the roles that mix specific entries with (or without) a wildcard: specific
// entries win over the wildcard, an empty verb list is an explicit denial that
// drops the resource entirely, and a role with no wildcard only ever covers
// the resources it names.
func TestExpandPermissions_RoleScopedExpansion(t *testing.T) {
	permissions := GetResourcePermissions()
	allResources := []string{
		"devices",
		"devices/console",
		"imageexports/download",
		"enrollmentrequests",
		"events",
	}

	tests := []struct {
		name           string
		role           string
		expectedOps    map[string][]string
		expectedAbsent []string
	}{
		{
			name: "When operator permissions are expanded it should keep specific entries and fill the rest from the wildcard",
			role: v1beta1.RoleOperator,
			expectedOps: map[string][]string{
				"devices":               {"get", "list", "create", "update", "patch", "delete"},
				"imageexports/download": {"get"},
				"devices/console":       {"get", "list"},
				"enrollmentrequests":    {"get", "list"},
				"events":                {"get", "list"},
			},
		},
		{
			name: "When viewer permissions are expanded it should omit every explicitly denied resource",
			role: v1beta1.RoleViewer,
			expectedOps: map[string][]string{
				"devices":            {"get", "list"},
				"enrollmentrequests": {"get", "list"},
				"events":             {"get", "list"},
			},
			expectedAbsent: []string{
				"devices/console",
				"devices/applications/console",
				"imageexports/download",
				"devices/enrollmenthooks/override",
			},
		},
		{
			name: "When installer permissions are expanded it should only cover the resources it names",
			role: v1beta1.RoleInstaller,
			expectedOps: map[string][]string{
				"enrollmentrequests":    {"get", "list"},
				"imageexports/download": {"get"},
			},
			expectedAbsent: []string{"devices", "devices/console", "events", "*"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			perms := permissions[tt.role]
			require.NotNil(t, perms, "role %q not found in resourcePermissions", tt.role)

			expanded := ExpandPermissions(perms, allResources)

			for resource, expectedOps := range tt.expectedOps {
				actualOps, exists := expanded[resource]
				require.True(t, exists, "expected resource %q in the expansion for role %q", resource, tt.role)
				assert.ElementsMatch(t, expectedOps, actualOps, "operations mismatch for resource %q", resource)
			}

			for _, resource := range tt.expectedAbsent {
				_, exists := expanded[resource]
				assert.False(t, exists, "resource %q should be absent from the expansion for role %q", resource, tt.role)
			}
		})
	}
}

// TestExpandPermissions_PrecedenceRules exercises the documented precedence
// rules directly, independently of the roles currently defined.
func TestExpandPermissions_PrecedenceRules(t *testing.T) {
	tests := []struct {
		name         string
		perms        map[string][]string
		allResources []string
		expected     map[string][]string
	}{
		{
			name:         "When a specific entry and a wildcard both apply it should prefer the specific entry",
			perms:        map[string][]string{"*": {"get", "list"}, "devices": {"get", "list", "delete"}},
			allResources: []string{"devices", "fleets"},
			expected:     map[string][]string{"devices": {"get", "list", "delete"}, "fleets": {"get", "list"}},
		},
		{
			name:         "When an entry has an empty verb list it should be denied and left out of the expansion",
			perms:        map[string][]string{"*": {"get", "list"}, "devices/console": {}},
			allResources: []string{"devices", "devices/console"},
			expected:     map[string][]string{"devices": {"get", "list"}},
		},
		{
			name:         "When there is no wildcard entry it should only expand the named resources",
			perms:        map[string][]string{"devices": {"get"}},
			allResources: []string{"devices", "fleets"},
			expected:     map[string][]string{"devices": {"get"}},
		},
		{
			name:         "When a named resource is missing from the resource list it should still be included",
			perms:        map[string][]string{"devices": {"get"}},
			allResources: []string{"fleets"},
			expected:     map[string][]string{"devices": {"get"}},
		},
		{
			name:         "When the wildcard grants all verbs alongside another entry it should expand per resource",
			perms:        map[string][]string{"*": {"*"}, "devices/console": {}},
			allResources: []string{"devices", "devices/console"},
			expected:     map[string][]string{"devices": {"*"}},
		},
		{
			name:         "When the permission map is empty it should expand to nothing",
			perms:        map[string][]string{},
			allResources: []string{"devices"},
			expected:     map[string][]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, ExpandPermissions(tt.perms, tt.allResources))
		})
	}
}

// TestResourcePermissions_ExplicitDenials pins down the resources the viewer
// role denies with an empty verb list, and confirms no other role expresses a
// denial that way.
func TestResourcePermissions_ExplicitDenials(t *testing.T) {
	permissions := GetResourcePermissions()

	deniedForViewer := []string{
		"devices/console",
		"devices/applications/console",
		"imageexports/download",
		"devices/enrollmenthooks/override",
	}

	viewerPerms := permissions[v1beta1.RoleViewer]
	require.NotNil(t, viewerPerms)

	for _, resource := range deniedForViewer {
		t.Run("When viewer permissions cover "+resource+" it should be an explicit denial with no verbs", func(t *testing.T) {
			ops, exists := viewerPerms[resource]
			require.True(t, exists, "expected an explicit entry for %q in the viewer role", resource)
			assert.Empty(t, ops, "expected %q to be denied with an empty verb list", resource)
		})
	}

	for _, role := range []string{v1beta1.RoleOrgAdmin, v1beta1.RoleAdmin, v1beta1.RoleOperator, v1beta1.RoleInstaller} {
		t.Run("When "+role+" permissions are inspected it should contain no empty verb lists", func(t *testing.T) {
			for resource, ops := range permissions[role] {
				assert.NotEmpty(t, ops, "role %q unexpectedly denies %q with an empty verb list", role, resource)
			}
		})
	}
}

// TestResourcePermissions_AllRolesDefined verifies every internal role that an
// external role maps to actually has a permission set, so a mapped identity
// can never end up holding a role the authorizer does not recognize.
func TestResourcePermissions_AllRolesDefined(t *testing.T) {
	permissions := GetResourcePermissions()

	roles := []string{
		v1beta1.RoleOrgAdmin,
		v1beta1.RoleAdmin,
		v1beta1.RoleOperator,
		v1beta1.RoleViewer,
		v1beta1.RoleInstaller,
	}

	for _, role := range roles {
		t.Run("When the "+role+" role is looked up it should have a permission set", func(t *testing.T) {
			perms, exists := permissions[role]
			require.True(t, exists, "role %q has no entry in resourcePermissions", role)
			assert.NotEmpty(t, perms, "role %q has an empty permission set", role)
		})
	}

	assert.Len(t, permissions, len(roles), "resourcePermissions contains roles beyond the known set")
}
