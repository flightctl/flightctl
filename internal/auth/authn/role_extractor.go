package authn

import (
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/sirupsen/logrus"
)

// RoleExtractor handles role extraction from claims based on role assignment configuration
type RoleExtractor struct {
	roleAssignment      api.AuthRoleAssignment
	log                 logrus.FieldLogger
	createdBySuperAdmin bool
}

// NewRoleExtractor creates a new role extractor with the given role assignment and super admin flag
func NewRoleExtractor(roleAssignment api.AuthRoleAssignment, createdBySuperAdmin bool, log logrus.FieldLogger) *RoleExtractor {
	return &RoleExtractor{
		roleAssignment:      roleAssignment,
		log:                 log,
		createdBySuperAdmin: createdBySuperAdmin,
	}
}

// ExtractRolesFromMap extracts roles from a map of claims (for OAuth2 userinfo)
// Deprecated: Use ExtractOrgRolesFromMap instead
func (r *RoleExtractor) ExtractRolesFromMap(claims map[string]interface{}) []string {
	orgRoles := r.ExtractOrgRolesFromMap(claims)
	// Flatten to simple list for backward compatibility
	allRoles := make(map[string]bool)
	for _, roles := range orgRoles {
		for _, role := range roles {
			allRoles[role] = true
		}
	}
	result := make([]string, 0, len(allRoles))
	for role := range allRoles {
		result = append(result, role)
	}
	return result
}

// ExtractOrgRolesFromMap extracts organization-scoped roles from claims
// Returns a map where:
// - Keys are organization names (or "*" for global roles)
// - Values are lists of roles for that organization
func (r *RoleExtractor) ExtractOrgRolesFromMap(claims map[string]interface{}) map[string][]string {
	discriminator, err := r.roleAssignment.Discriminator()
	if err != nil {
		r.log.Errorf("RoleExtractor: failed to get discriminator: %v", err)
		return nil
	}

	switch discriminator {
	case string(api.AuthStaticRoleAssignmentTypeStatic):
		return r.extractStaticOrgRoles()
	case string(api.AuthDynamicRoleAssignmentTypeDynamic):
		return r.extractDynamicOrgRolesFromMap(claims)
	default:
		r.log.Warnf("RoleExtractor: unknown discriminator: %s", discriminator)
		return nil
	}
}

// extractStaticOrgRoles returns static roles as global roles (apply to all orgs)
func (r *RoleExtractor) extractStaticOrgRoles() map[string][]string {
	staticRoleAssignment, err := r.roleAssignment.AsAuthStaticRoleAssignment()
	if err != nil {
		return nil
	}
	// Static roles are global - apply to all organizations
	return map[string][]string{
		"*": staticRoleAssignment.Roles,
	}
}

// extractDynamicOrgRolesFromMap extracts org-scoped roles from claims using separator
func (r *RoleExtractor) extractDynamicOrgRolesFromMap(claims map[string]interface{}) map[string][]string {
	dynamicRoleAssignment, err := r.roleAssignment.AsAuthDynamicRoleAssignment()
	if err != nil {
		r.log.Errorf("RoleExtractor: failed to get dynamic role assignment: %v", err)
		return nil
	}

	claimPath := dynamicRoleAssignment.ClaimPath
	if len(claimPath) == 0 {
		r.log.Warnf("RoleExtractor: claimPath is empty")
		return nil
	}

	// Get separator (default to ":")
	separator := ":"
	if dynamicRoleAssignment.Separator != nil && *dynamicRoleAssignment.Separator != "" {
		separator = *dynamicRoleAssignment.Separator
	}

	// Navigate to the claim value
	current := claims
	for i, part := range claimPath {
		r.log.Debugf("RoleExtractor: navigating claimPath[%d]=%s, current keys: %v", i, part, getMapKeys(current))
		if i == len(claimPath)-1 {
			// Last part - extract the roles array
			if value, exists := current[part]; exists {
				if roles, ok := value.([]interface{}); ok {
					return r.parseRolesWithSeparator(roles, separator)
				}
				r.log.Warnf("RoleExtractor: claim value is not an array: %T", value)
			} else {
				r.log.Warnf("RoleExtractor: claim path[%d]=%s does not exist in current map", i, part)
			}
			return nil
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
				r.log.Debugf("RoleExtractor: navigated to nested map at path[%d]=%s", i, part)
			} else {
				r.log.Warnf("RoleExtractor: path[%d]=%s exists but is not a map: %T", i, part, next)
				return nil
			}
		} else {
			r.log.Warnf("RoleExtractor: path[%d]=%s does not exist", i, part)
			return nil
		}
	}

	return nil
}

// getMapKeys is a helper to get keys from a map for logging
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// parseRolesWithSeparator parses role strings and separates org-scoped from global roles
func (r *RoleExtractor) parseRolesWithSeparator(roles []interface{}, separator string) map[string][]string {
	orgRoles := make(map[string][]string)

	for _, role := range roles {
		roleStr, ok := role.(string)
		if !ok || roleStr == "" {
			continue
		}

		// If AuthProvider was not created by super admin, filter out flightctl-admin role
		if !r.createdBySuperAdmin {
			// Check if role is flightctl-admin (either as global or org-scoped)
			if roleStr == api.ExternalRoleAdmin {
				// Global flightctl-admin role - skip it
				r.log.Debugf("RoleExtractor: filtering out flightctl-admin role (AP not created by super admin)")
				continue
			}
			// Check if org-scoped role contains flightctl-admin
			if strings.Contains(roleStr, separator) {
				parts := strings.SplitN(roleStr, separator, 2)
				if len(parts) == 2 && parts[1] == api.ExternalRoleAdmin {
					// Org-scoped flightctl-admin role - skip it
					r.log.Debugf("RoleExtractor: filtering out org-scoped flightctl-admin role (AP not created by super admin)")
					continue
				}
			}
		}

		// Check if role contains separator
		if strings.Contains(roleStr, separator) {
			// Org-scoped role: "org1:role1"
			parts := strings.SplitN(roleStr, separator, 2)
			if len(parts) == 2 {
				orgName := parts[0]
				roleName := parts[1]
				if orgName != "" && roleName != "" {
					orgRoles[orgName] = append(orgRoles[orgName], roleName)
				}
			}
		} else {
			// Global role: "role1"
			orgRoles["*"] = append(orgRoles["*"], roleStr)
		}
	}

	return orgRoles
}

// ValidateRoleAssignment validates a role assignment configuration
func ValidateRoleAssignment(roleAssignment api.AuthRoleAssignment) error {
	discriminator, err := roleAssignment.Discriminator()
	if err != nil {
		return fmt.Errorf("invalid role assignment: %w", err)
	}

	switch discriminator {
	case string(api.AuthStaticRoleAssignmentTypeStatic):
		staticRoleAssignment, err := roleAssignment.AsAuthStaticRoleAssignment()
		if err != nil {
			return fmt.Errorf("invalid static role assignment: %w", err)
		}
		if len(staticRoleAssignment.Roles) == 0 {
			return fmt.Errorf("static role assignment must have at least one role")
		}
	case string(api.AuthDynamicRoleAssignmentTypeDynamic):
		dynamicRoleAssignment, err := roleAssignment.AsAuthDynamicRoleAssignment()
		if err != nil {
			return fmt.Errorf("invalid dynamic role assignment: %w", err)
		}
		if len(dynamicRoleAssignment.ClaimPath) == 0 {
			return fmt.Errorf("dynamic role assignment must have a claim path")
		}
	default:
		return fmt.Errorf("unknown role assignment type: %s", discriminator)
	}

	return nil
}
