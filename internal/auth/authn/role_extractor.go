package authn

import (
	"fmt"

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
func (r *RoleExtractor) ExtractRolesFromMap(claims map[string]interface{}) []string {
	discriminator, err := r.roleAssignment.Discriminator()
	if err != nil {
		return []string{}
	}

	switch discriminator {
	case string(api.AuthStaticRoleAssignmentTypeStatic):
		return r.extractStaticRoles()
	case string(api.AuthDynamicRoleAssignmentTypeDynamic):
		return r.extractDynamicRolesFromMap(claims)
	default:
		return []string{}
	}
}

// extractStaticRoles returns the static roles from the role assignment
func (r *RoleExtractor) extractStaticRoles() []string {
	staticRoleAssignment, err := r.roleAssignment.AsAuthStaticRoleAssignment()
	if err != nil {
		return []string{}
	}
	return staticRoleAssignment.Roles
}

// extractDynamicRolesFromMap extracts roles from a map using the claim path
func (r *RoleExtractor) extractDynamicRolesFromMap(claims map[string]interface{}) []string {
	dynamicRoleAssignment, err := r.roleAssignment.AsAuthDynamicRoleAssignment()
	if err != nil {
		return []string{}
	}

	claimPath := dynamicRoleAssignment.ClaimPath
	if len(claimPath) == 0 {
		return []string{}
	}

	current := claims

	for i, part := range claimPath {
		if i == len(claimPath)-1 {
			// Last part - extract the roles array
			if value, exists := current[part]; exists {
				if roles, ok := value.([]interface{}); ok {
					var result []string
					for _, role := range roles {
						if str, ok := role.(string); ok {
							result = append(result, str)
						}
					}
					return result
				}
			}
			return []string{}
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return []string{}
			}
		} else {
			return []string{}
		}
	}

	return []string{}
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
