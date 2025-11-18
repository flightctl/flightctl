package authz

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/sirupsen/logrus"
)

// StaticAuthZ implements role-based authorization using system groups
type StaticAuthZ struct {
	log logrus.FieldLogger
}

// Resource permissions based on K8s RBAC roles
var resourcePermissions = map[string]map[string][]string{
	v1alpha1.RoleAdmin: {
		"*": {"*"}, // Admin has access to all resources and all operations
	},
	v1alpha1.RoleOperator: {
		"devices":       {"create", "update", "patch", "delete"},
		"fleets":        {"create", "update", "patch", "delete"},
		"resourcesyncs": {"create", "update", "patch", "delete"},
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

	// Get user's roles from the mapped identity
	userRoles := mappedIdentity.GetRoles()
	s.log.Debugf("StaticAuthZ: checking permission for user=%s, resource=%s, op=%s, roles=%v",
		mappedIdentity.GetUsername(), resource, op, userRoles)

	for _, role := range userRoles {
		if permissions, exists := resourcePermissions[role]; exists {
			// First check for wildcard resource (*) - gives access to all resources
			if resourcePerms, exists := permissions["*"]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted via wildcard resource for user=%s, role=%s, resource=%s, op=%s",
							mappedIdentity.GetUsername(), role, resource, op)
						return true, nil
					}
				}
			}

			// Then check for specific resource
			if resourcePerms, exists := permissions[resource]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted for user=%s, role=%s, resource=%s, op=%s",
							mappedIdentity.GetUsername(), role, resource, op)
						return true, nil
					}
				}
			}
		}
	}
	s.log.Debugf("StaticAuthZ: permission denied for user=%s, resource=%s, op=%s, roles=%v",
		mappedIdentity.GetUsername(), resource, op, userRoles)
	return false, nil
}
