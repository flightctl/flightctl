package authz

import (
	"context"
	"fmt"
	"sort"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
)

// StaticAuthZ implements role-based authorization using system groups
type StaticAuthZ struct {
	log logrus.FieldLogger
}

// Resource permissions based on K8s RBAC roles
var resourcePermissions = map[string]map[string][]string{
	v1beta1.RoleOrgAdmin: {
		"*": {"*"}, // Org admin has access to all resources and all operations within their organization
	},
	v1beta1.RoleAdmin: {
		"*": {"*"}, // Admin has access to all resources and all operations
	},
	v1beta1.RoleOperator: {
		"devices":       {"create", "update", "patch", "delete"},
		"fleets":        {"create", "update", "patch", "delete"},
		"resourcesyncs": {"create", "update", "patch", "delete"},
		"repositories":  {"create", "update", "patch", "delete"},
		"*":             {"get", "list"},
	},
	v1beta1.RoleViewer: {
		"*": {"get", "list"},
	},
	v1beta1.RoleInstaller: {
		"enrollmentrequests":          {"get", "list"},
		"enrollmentrequests/approval": {"update"},
		"organizations":               {"get", "list"},
		"certificatesigningrequests":  {"get", "list", "create", "update"},
	},
}

func NewStaticAuthZ(log logrus.FieldLogger) *StaticAuthZ {
	return &StaticAuthZ{
		log: log,
	}
}

func (s StaticAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		s.log.Debug("StaticAuthZ: no mapped identity found in context")
		return false, fmt.Errorf("no mapped identity found in context")
	}

	s.log.Debugf("StaticAuthZ: checking permission for user=%s, resource=%s, op=%s",
		mappedIdentity.GetUsername(), resource, op)

	// 1. Super admins have access to everything
	if mappedIdentity.IsSuperAdmin() {
		s.log.Debugf("StaticAuthZ: permission granted for super admin user=%s, resource=%s, op=%s",
			mappedIdentity.GetUsername(), resource, op)
		return true, nil
	}

	// 2. Get the selected organization from context
	orgUUID, ok := util.GetOrgIdFromContext(ctx)
	if !ok {
		s.log.Debug("StaticAuthZ: no organization ID found in context")
		return false, fmt.Errorf("no organization ID found in context")
	}
	orgID := orgUUID.String()

	// 3. Get user's roles for the selected organization only
	roles := mappedIdentity.GetRolesForOrg(orgID)
	if len(roles) == 0 {
		s.log.Debugf("StaticAuthZ: user=%s has no roles in organization=%s",
			mappedIdentity.GetUsername(), orgID)
		return false, nil
	}

	// Check if any of the user's roles in this org grant the required permission
	for _, role := range roles {
		if permissions, exists := resourcePermissions[role]; exists {
			// First check for wildcard resource (*) - gives access to all resources
			if resourcePerms, exists := permissions["*"]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted via wildcard resource for user=%s, role=%s, org=%s, resource=%s, op=%s",
							mappedIdentity.GetUsername(), role, orgID, resource, op)
						return true, nil
					}
				}
			}

			// Then check for specific resource
			if resourcePerms, exists := permissions[resource]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted for user=%s, role=%s, org=%s, resource=%s, op=%s",
							mappedIdentity.GetUsername(), role, orgID, resource, op)
						return true, nil
					}
				}
			}
		}
	}

	s.log.Debugf("StaticAuthZ: permission denied for user=%s, org=%s, resource=%s, op=%s",
		mappedIdentity.GetUsername(), orgID, resource, op)
	return false, nil
}

func (s StaticAuthZ) GetUserPermissions(ctx context.Context) (*v1beta1.PermissionList, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		s.log.Debug("StaticAuthZ: no mapped identity found in context")
		return nil, fmt.Errorf("no mapped identity found in context")
	}

	s.log.Debugf("StaticAuthZ: getting permissions for user=%s", mappedIdentity.GetUsername())

	// Super admins have all permissions
	var userRoles []string
	if mappedIdentity.IsSuperAdmin() {
		s.log.Debugf("StaticAuthZ: user=%s is super admin, granting all permissions", mappedIdentity.GetUsername())
		userRoles = []string{v1beta1.RoleAdmin}
	} else {
		// Get the selected organization from context
		orgUUID, ok := util.GetOrgIdFromContext(ctx)
		if !ok {
			s.log.Debug("StaticAuthZ: no organization ID found in context")
			return nil, fmt.Errorf("no organization ID found in context")
		}
		orgID := orgUUID.String()

		// Get user's roles for the selected organization only
		userRoles = mappedIdentity.GetRolesForOrg(orgID)
		if len(userRoles) == 0 {
			s.log.Debugf("StaticAuthZ: user=%s has no roles in organization=%s",
				mappedIdentity.GetUsername(), orgID)
			return &v1beta1.PermissionList{Permissions: []v1beta1.Permission{}}, nil
		}
		s.log.Debugf("StaticAuthZ: getting permissions for user=%s, org=%s, roles=%v",
			mappedIdentity.GetUsername(), orgID, userRoles)
	}

	// Merge permissions from all roles
	mergedPermissions := make(map[string][]string)
	for _, role := range userRoles {
		if permissions, exists := resourcePermissions[role]; exists {
			for resource, ops := range permissions {
				if existingOps, exists := mergedPermissions[resource]; exists {
					// Merge operations, avoiding duplicates
					opsMap := make(map[string]bool)
					for _, op := range existingOps {
						opsMap[op] = true
					}
					for _, op := range ops {
						opsMap[op] = true
					}
					mergedOps := make([]string, 0, len(opsMap))
					for op := range opsMap {
						mergedOps = append(mergedOps, op)
					}
					mergedPermissions[resource] = mergedOps
				} else {
					// Copy operations slice to avoid sharing
					opsCopy := make([]string, len(ops))
					copy(opsCopy, ops)
					mergedPermissions[resource] = opsCopy
				}
			}
		}
	}

	// Convert to API format with sorted resources
	resources := make([]string, 0, len(mergedPermissions))
	for resource := range mergedPermissions {
		resources = append(resources, resource)
	}
	sort.Strings(resources)

	apiPermissions := make([]v1beta1.Permission, 0, len(mergedPermissions))
	for _, resource := range resources {
		ops := mergedPermissions[resource]
		// Sort operations for consistent output
		sort.Strings(ops)

		apiPermissions = append(apiPermissions, v1beta1.Permission{
			Resource:   resource,
			Operations: ops,
		})
	}

	s.log.Debugf("StaticAuthZ: returning %d permissions for user=%s", len(apiPermissions), mappedIdentity.GetUsername())
	return &v1beta1.PermissionList{
		Permissions: apiPermissions,
	}, nil
}
