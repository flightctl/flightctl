package authn

import (
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// RoleExtractor handles role extraction from claims based on role assignment configuration
type RoleExtractor struct {
	roleAssignment api.AuthRoleAssignment
}

// NewRoleExtractor creates a new role extractor with the given role assignment
func NewRoleExtractor(roleAssignment api.AuthRoleAssignment) *RoleExtractor {
	return &RoleExtractor{
		roleAssignment: roleAssignment,
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
		return nil
	}

	switch discriminator {
	case string(api.AuthStaticRoleAssignmentTypeStatic):
		return r.extractStaticOrgRoles()
	case string(api.AuthDynamicRoleAssignmentTypeDynamic):
		return r.extractDynamicOrgRolesFromMap(claims)
	default:
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
		return nil
	}

	claimPath := dynamicRoleAssignment.ClaimPath
	if len(claimPath) == 0 {
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
		if i == len(claimPath)-1 {
			// Last part - extract the roles array
			if value, exists := current[part]; exists {
				if roles, ok := value.([]interface{}); ok {
					return r.parseRolesWithSeparator(roles, separator)
				}
			}
			return nil
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return nil
			}
		} else {
			return nil
		}
	}

	return nil
}

// parseRolesWithSeparator parses role strings and separates org-scoped from global roles
func (r *RoleExtractor) parseRolesWithSeparator(roles []interface{}, separator string) map[string][]string {
	orgRoles := make(map[string][]string)

	for _, role := range roles {
		roleStr, ok := role.(string)
		if !ok || roleStr == "" {
			continue
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
