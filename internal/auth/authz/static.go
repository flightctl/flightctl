package authz

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/contextutil"
)

// StaticAuthZ implements role-based authorization using system groups
type StaticAuthZ struct {
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

func NewStaticAuthZ() *StaticAuthZ {
	return &StaticAuthZ{}
}

func (s StaticAuthZ) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	// Get mapped identity from context (set by identity mapping middleware)
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok {
		return false, fmt.Errorf("no mapped identity found in context")
	}

	// Get user's roles from the mapped identity
	userRoles := mappedIdentity.GetRoles()

	for _, role := range userRoles {
		if permissions, exists := resourcePermissions[role]; exists {
			// First check for wildcard resource (*) - gives access to all resources
			if resourcePerms, exists := permissions["*"]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						return true, nil
					}
				}
			}

			// Then check for specific resource
			if resourcePerms, exists := permissions[resource]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}
