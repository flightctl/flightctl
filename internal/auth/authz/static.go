package authz

import (
	"context"
	"fmt"
	"slices"
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

// resourcePermissions defines role-based access control for all API resources.
//
// Precedence rules (mirrored by ExpandPermissions and CheckPermission):
//  1. A specific resource entry always overrides the wildcard "*".
//  2. An empty verb list (e.g. "resource": {}) is an explicit denial.
//  3. If no specific entry exists, the wildcard verbs apply.
var resourcePermissions = map[string]map[string][]string{
	v1beta1.RoleOrgAdmin: {
		"*": {"*"}, // Org admin has access to all resources and all operations within their organization
	},
	v1beta1.RoleAdmin: {
		"*": {"*"}, // Admin has access to all resources and all operations
	},
	v1beta1.RoleOperator: {
		// Operator has full CRUD on these resources (specific entries override wildcard)
		"devices":                        {"get", "list", "create", "update", "patch", "delete"},
		"fleets":                         {"get", "list", "create", "update", "patch", "delete"},
		"resourcesyncs":                  {"get", "list", "create", "update", "patch", "delete"},
		"repositories":                   {"get", "list", "create", "update", "patch", "delete"},
		"catalogs":                       {"get", "list"},
		"catalogitems":                   {"get", "list"},
		"imagebuilds":                    {"get", "list", "create", "update", "patch", "delete"},
		"imagebuilds/cancel":             {"create"},
		"imagebuilds/newversion":         {"create"},
		"imageexports":                   {"get", "list", "create", "update", "patch", "delete"},
		"imageexports/cancel":            {"create"},
		"imageexports/download":          {"get"},
		"imagepromotions":                {"get", "list", "create", "update", "patch", "delete"},
		"repositories/check-oci-tag":     {"create"},
		"repositories/check-oci-image":   {"create"},
		"devices/applications/lifecycle": {"update"},      // stop/start/restart a device's application
		"fleets/applications/lifecycle":  {"update"},      // stop/start an application across a fleet
		"*":                              {"get", "list"}, // Default read access for other resources
	},
	v1beta1.RoleViewer: {
		"*":                            {"get", "list"}, // Default read access to all resources
		"devices/console":              {},              // Explicitly denied - console access requires operator or admin role
		"devices/applications/console": {},              // Explicitly denied - console access requires operator or admin role
		"imageexports/download":        {},              // Explicitly denied - empty list overrides wildcard
	},
	v1beta1.RoleInstaller: {
		"enrollmentrequests":          {"get", "list"},
		"enrollmentrequests/approval": {"update"},
		"organizations":               {"get", "list"},
		"certificatesigningrequests":  {"get", "list", "create", "update"},
		"imagebuilds":                 {"get", "list"},
		"imageexports":                {"get", "list"},
		"imageexports/download":       {"get"},
	},
}

// syntheticResources lists CheckPermission resource names that have no
// corresponding OpenAPI x-resource annotation (e.g. resources gated by
// internal proxies rather than API routes). The genroles generator unions
// this list with OpenAPI-derived resources so wildcard ("*") role permissions
// expand to include them in the generated K8s ClusterRoles.
var syntheticResources = []string{
	"alerts", // gated by cmd/flightctl-alertmanager-proxy
}

// GetSyntheticResources returns a copy of the synthetic resource list.
func GetSyntheticResources() []string {
	return slices.Clone(syntheticResources)
}

// GetResourcePermissions returns a deep copy of the role-based permission map.
// This is used by the Helm ClusterRole generator to produce K8s RBAC rules
// that match the static authorization logic.
func GetResourcePermissions() map[string]map[string][]string {
	result := make(map[string]map[string][]string, len(resourcePermissions))
	for role, resources := range resourcePermissions {
		resCopy := make(map[string][]string, len(resources))
		for resource, ops := range resources {
			resCopy[resource] = slices.Clone(ops)
		}
		result[role] = resCopy
	}
	return result
}

// ExpandPermissions resolves a wildcard-based permission map into an explicit
// resource-to-verbs mapping. It replicates the precedence logic from
// CheckPermission: specific entries override the wildcard, and an empty verb
// list means explicit denial. This is used by both the genroles generator and
// the round-trip correctness test to ensure a single expansion implementation.
func ExpandPermissions(perms map[string][]string, allResources []string) map[string][]string {
	wildcardOps := perms["*"]

	isWildcardOps := len(wildcardOps) == 1 && wildcardOps[0] == "*"
	if isWildcardOps && len(perms) == 1 {
		return map[string][]string{"*": {"*"}}
	}

	result := make(map[string][]string)
	for _, resource := range allResources {
		if ops, exists := perms[resource]; exists {
			if len(ops) == 0 {
				continue
			}
			result[resource] = ops
		} else if len(wildcardOps) > 0 {
			result[resource] = wildcardOps
		}
	}

	for resource, ops := range perms {
		if resource == "*" {
			continue
		}
		if len(ops) == 0 {
			continue
		}
		if _, exists := result[resource]; !exists {
			result[resource] = ops
		}
	}

	return result
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
			// First check for specific resource - if it exists, it takes precedence over wildcard
			// This allows explicit denials via empty permission lists
			if resourcePerms, exists := permissions[resource]; exists {
				// If the resource has an empty permission list, it's an explicit denial - skip to next role
				if len(resourcePerms) == 0 {
					s.log.Debugf("StaticAuthZ: permission explicitly denied for user=%s, role=%s, org=%s, resource=%s, op=%s",
						mappedIdentity.GetUsername(), role, orgID, resource, op)
					continue
				}
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted for user=%s, role=%s, org=%s, resource=%s, op=%s",
							mappedIdentity.GetUsername(), role, orgID, resource, op)
						return true, nil
					}
				}
				// Specific resource entry exists but doesn't grant the operation - continue to check other roles
				continue
			}

			// No specific resource entry - check for wildcard resource (*) - gives access to all resources
			if resourcePerms, exists := permissions["*"]; exists {
				for _, allowedOp := range resourcePerms {
					if allowedOp == "*" || allowedOp == op {
						s.log.Debugf("StaticAuthZ: permission granted via wildcard resource for user=%s, role=%s, org=%s, resource=%s, op=%s",
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
