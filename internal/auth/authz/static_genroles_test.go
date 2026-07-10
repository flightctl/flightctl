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

// TestExpandedPermissionsMatchCheckPermission verifies that expanding the
// wildcard-based resourcePermissions map produces results identical to what
// StaticAuthZ.CheckPermission returns at runtime. This is the correctness
// guarantee for the genroles Helm ClusterRole generator: if this test passes,
// the generated K8s RBAC rules grant exactly the same access as static auth.
func TestExpandedPermissionsMatchCheckPermission(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.WarnLevel)
	authZ := NewStaticAuthZ(log)

	allVerbs := []string{"get", "list", "create", "update", "patch", "delete"}

	permissions := GetResourcePermissions()

	rolesToTest := []string{
		v1beta1.RoleOperator,
		v1beta1.RoleViewer,
		v1beta1.RoleInstaller,
	}

	allResources := collectAllResources(permissions)

	for _, role := range rolesToTest {
		perms := permissions[role]
		require.NotNil(t, perms, "role %q not found in resourcePermissions", role)

		expanded := ExpandPermissions(perms, allResources)

		t.Run(role, func(t *testing.T) {
			for _, resource := range allResources {
				for _, verb := range allVerbs {
					expandedAllows := verbInList(expanded[resource], verb)
					runtimeAllows := checkPermissionForRole(t, authZ, role, resource, verb)

					assert.Equal(t, runtimeAllows, expandedAllows,
						"role=%s resource=%s verb=%s: CheckPermission=%v expanded=%v",
						role, resource, verb, runtimeAllows, expandedAllows)
				}
			}
		})
	}
}

func verbInList(ops []string, verb string) bool {
	for _, op := range ops {
		if op == "*" || op == verb {
			return true
		}
	}
	return false
}

func collectAllResources(permissions map[string]map[string][]string) []string {
	seen := make(map[string]struct{})
	for _, perms := range permissions {
		for resource := range perms {
			if resource != "*" {
				seen[resource] = struct{}{}
			}
		}
	}

	resources := make([]string, 0, len(seen))
	for r := range seen {
		resources = append(resources, r)
	}
	return resources
}

func checkPermissionForRole(t *testing.T, authZ *StaticAuthZ, role, resource, verb string) bool {
	t.Helper()

	orgID := uuid.New()
	testOrg := &model.Organization{
		ID:          orgID,
		ExternalID:  "test-org",
		DisplayName: "Test Organization",
	}
	orgRoles := map[string][]string{orgID.String(): {role}}
	mappedIdentity := identity.NewMappedIdentity("testuser", "testuser", []*model.Organization{testOrg}, orgRoles, false, nil)

	ctx := context.WithValue(context.Background(), consts.MappedIdentityCtxKey, mappedIdentity)
	ctx = util.WithOrganizationID(ctx, orgID)

	allowed, err := authZ.CheckPermission(ctx, resource, verb)
	require.NoError(t, err)
	return allowed
}
