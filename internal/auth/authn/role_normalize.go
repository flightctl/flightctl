package authn

import (
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
)

// normalizeRoleName strips the role suffix from a role name if present.
// If roleSuffix is provided and the role name ends with "-<roleSuffix>", it strips the suffix.
// Returns the normalized role name or the original if no suffix match.
func normalizeRoleName(roleName string, roleSuffix *string) string {
	if roleSuffix == nil || *roleSuffix == "" {
		return roleName
	}

	suffix := "-" + *roleSuffix
	if strings.HasSuffix(roleName, suffix) {
		normalized := strings.TrimSuffix(roleName, suffix)
		// Validate that the normalized role is a known external role
		knownRoles := []string{
			api.ExternalRoleAdmin,
			api.ExternalRoleOrgAdmin,
			api.ExternalRoleOperator,
			api.ExternalRoleViewer,
			api.ExternalRoleInstaller,
		}
		for _, knownRole := range knownRoles {
			if normalized == knownRole {
				return normalized
			}
		}
		// If not a known role, return original (might be a custom role)
		return roleName
	}

	return roleName
}

// normalizeRoleNames normalizes a slice of role names by stripping the role suffix if present.
func normalizeRoleNames(roleNames []string, roleSuffix *string) []string {
	if roleSuffix == nil || *roleSuffix == "" {
		return roleNames
	}

	normalized := make([]string, 0, len(roleNames))
	for _, roleName := range roleNames {
		normalized = append(normalized, normalizeRoleName(roleName, roleSuffix))
	}
	return normalized
}
