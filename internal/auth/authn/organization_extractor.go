package authn

import (
	api "github.com/flightctl/flightctl/api/core/v1beta1"
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

	// If no org config, return empty
	if e.orgConfig == nil || e.orgConfig.OrganizationAssignment == nil {
		return organizations
	}

	assignment := e.orgConfig.OrganizationAssignment

	// Get the discriminator to determine which type we have
	discriminator, err := assignment.Discriminator()
	if err != nil {
		return organizations
	}

	switch discriminator {
	case string(api.AuthStaticOrganizationAssignmentTypeStatic):
		return e.extractStaticOrganization(assignment)
	case string(api.AuthDynamicOrganizationAssignmentTypeDynamic):
		return e.extractDynamicOrganizations(assignment, claims)
	case string(api.PerUser):
		return e.extractPerUserOrganization(assignment, username)
	}

	return organizations
}

// extractStaticOrganization handles static organization assignment
func (e *OrganizationExtractor) extractStaticOrganization(assignment *api.AuthOrganizationAssignment) []string {
	var organizations []string
	staticAssignment, err := assignment.AsAuthStaticOrganizationAssignment()
	if err != nil {
		return organizations
	}
	if staticAssignment.OrganizationName == "" {
		return organizations
	}
	organizations = append(organizations, staticAssignment.OrganizationName)
	return organizations
}

// extractDynamicOrganizations handles dynamic organization assignment from claims
func (e *OrganizationExtractor) extractDynamicOrganizations(assignment *api.AuthOrganizationAssignment, claims map[string]interface{}) []string {
	var organizations []string
	dynamicAssignment, err := assignment.AsAuthDynamicOrganizationAssignment()
	if err != nil {
		return organizations
	}
	if len(dynamicAssignment.ClaimPath) == 0 {
		return organizations
	}

	orgValue, exists := e.getValueByPath(claims, dynamicAssignment.ClaimPath)
	if !exists {
		return organizations
	}

	// Handle single string value
	if orgStr, ok := orgValue.(string); ok && orgStr != "" {
		orgName := e.applyPrefixSuffix(orgStr, dynamicAssignment.OrganizationNamePrefix, dynamicAssignment.OrganizationNameSuffix)
		organizations = append(organizations, orgName)
		return organizations
	}

	// Handle array of values
	orgArray, ok := orgValue.([]interface{})
	if !ok {
		return organizations
	}

	for _, item := range orgArray {
		orgStr, ok := item.(string)
		if !ok || orgStr == "" {
			continue
		}
		orgName := e.applyPrefixSuffix(orgStr, dynamicAssignment.OrganizationNamePrefix, dynamicAssignment.OrganizationNameSuffix)
		organizations = append(organizations, orgName)
	}

	return organizations
}

// extractPerUserOrganization handles per-user organization assignment
func (e *OrganizationExtractor) extractPerUserOrganization(assignment *api.AuthOrganizationAssignment, username string) []string {
	var organizations []string
	perUserAssignment, err := assignment.AsAuthPerUserOrganizationAssignment()
	if err != nil {
		return organizations
	}
	if username == "" {
		return organizations
	}
	orgName := e.applyPrefixSuffix(username, perUserAssignment.OrganizationNamePrefix, perUserAssignment.OrganizationNameSuffix)
	organizations = append(organizations, orgName)
	return organizations
}

// applyPrefixSuffix applies prefix and suffix to an organization name
func (e *OrganizationExtractor) applyPrefixSuffix(orgName string, prefix *string, suffix *string) string {
	if prefix != nil && *prefix != "" {
		orgName = *prefix + orgName
	}
	if suffix != nil && *suffix != "" {
		orgName = orgName + *suffix
	}
	return orgName
}

// getValueByPath extracts a value from claims using an array path
func (e *OrganizationExtractor) getValueByPath(claims map[string]interface{}, path []string) (interface{}, bool) {
	if len(path) == 0 {
		return nil, false
	}

	current := claims

	for i, part := range path {
		if i == len(path)-1 {
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
