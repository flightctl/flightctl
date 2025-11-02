package authn

import (
	"strings"

	"github.com/flightctl/flightctl/internal/auth/common"
)

// OrganizationExtractor provides shared organization extraction logic for both OIDC and OAuth2 providers
type OrganizationExtractor struct {
	orgConfig *common.AuthOrganizationsConfig
}

// NewOrganizationExtractor creates a new organization extractor
func NewOrganizationExtractor(orgConfig *common.AuthOrganizationsConfig) *OrganizationExtractor {
	return &OrganizationExtractor{
		orgConfig: orgConfig,
	}
}

// ExtractOrganizations extracts organization information based on org config
func (e *OrganizationExtractor) ExtractOrganizations(claims map[string]interface{}, username string) []string {
	var organizations []string

	// If no org config or organizations are disabled, return empty
	if e.orgConfig == nil || !e.orgConfig.Enabled || e.orgConfig.OrganizationAssignment == nil {
		return organizations
	}

	assignment := e.orgConfig.OrganizationAssignment

	switch assignment.Type {
	case OrganizationAssignmentTypeStatic:
		// Static assignment: use the configured organization name
		if assignment.OrganizationName != nil && *assignment.OrganizationName != "" {
			organizations = append(organizations, *assignment.OrganizationName)
		}
	case OrganizationAssignmentTypeDynamic:
		// Dynamic assignment: extract from claim and apply prefix/suffix
		if assignment.ClaimPath != nil && *assignment.ClaimPath != "" {
			if orgValue, exists := e.getValueByPath(claims, *assignment.ClaimPath); exists {
				// Handle both string and array values
				if orgStr, ok := orgValue.(string); ok && orgStr != "" {
					// Single string value
					orgName := orgStr
					if assignment.OrganizationNamePrefix != nil && *assignment.OrganizationNamePrefix != "" {
						orgName = *assignment.OrganizationNamePrefix + orgName
					}
					if assignment.OrganizationNameSuffix != nil && *assignment.OrganizationNameSuffix != "" {
						orgName = orgName + *assignment.OrganizationNameSuffix
					}
					organizations = append(organizations, orgName)
				} else if orgArray, ok := orgValue.([]interface{}); ok {
					// Array of values
					for _, item := range orgArray {
						if orgStr, ok := item.(string); ok && orgStr != "" {
							orgName := orgStr
							if assignment.OrganizationNamePrefix != nil && *assignment.OrganizationNamePrefix != "" {
								orgName = *assignment.OrganizationNamePrefix + orgName
							}
							if assignment.OrganizationNameSuffix != nil && *assignment.OrganizationNameSuffix != "" {
								orgName = orgName + *assignment.OrganizationNameSuffix
							}
							organizations = append(organizations, orgName)
						}
					}
				}
			}
		}
	case OrganizationAssignmentTypePerUser:
		// Per-user assignment: create organization name from username
		if username != "" {
			orgName := username
			if assignment.OrganizationNamePrefix != nil && *assignment.OrganizationNamePrefix != "" {
				orgName = *assignment.OrganizationNamePrefix + orgName
			}
			if assignment.OrganizationNameSuffix != nil && *assignment.OrganizationNameSuffix != "" {
				orgName = orgName + *assignment.OrganizationNameSuffix
			}
			organizations = append(organizations, orgName)
		}
	}

	return organizations
}

// getValueByPath extracts a value from claims using dot notation path
func (e *OrganizationExtractor) getValueByPath(claims map[string]interface{}, path string) (interface{}, bool) {
	if path == "" {
		return nil, false
	}

	// Split the path by dots
	parts := strings.Split(path, ".")
	current := claims

	for i, part := range parts {
		if i == len(parts)-1 {
			// Last part - extract the value
			if value, exists := current[part]; exists {
				return value, true
			}
			return nil, false
		}

		// Navigate deeper into the object
		if next, exists := current[part]; exists {
			if nextMap, ok := next.(map[string]interface{}); ok {
				current = nextMap
			} else {
				return nil, false
			}
		} else {
			return nil, false
		}
	}

	return nil, false
}
