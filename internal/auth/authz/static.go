package authz

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
	v1alpha1.RoleOrgAdmin: {
		"*": {"*"}, // Org admin has access to all resources and all operations within their organization
	},
	v1alpha1.RoleAdmin: {
		"*": {"*"}, // Admin has access to all resources and all operations
	},
	v1alpha1.RoleOperator: {
		"devices":       {"create", "update", "patch", "delete"},
		"fleets":        {"create", "update", "patch", "delete"},
		"resourcesyncs": {"create", "update", "patch", "delete"},
		"repositories":  {"create", "update", "patch", "delete"},
		"*":             {"get", "list"},
	},
	v1alpha1.RoleViewer: {
		"*": {"get", "list"},
	},
	v1alpha1.RoleInstaller: {
		"devices":      {"get", "list"},
		"fleets":       {"get", "list"},
		"repositories": {"get", "list"},
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
