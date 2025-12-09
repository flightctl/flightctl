package authn

import (
	"testing"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoleExtractor_FilterFlightctlAdmin_NotCreatedBySuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create role assignment with dynamic role mapping
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
	}
	err := roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	require.NoError(t, err)

	// Create extractor with createdBySuperAdmin=false
	extractor := NewRoleExtractor(roleAssignment, false, log)

	// Test claims with flightctl-admin role
	claims := map[string]interface{}{
		"roles": []interface{}{
			api.ExternalRoleAdmin,
			api.ExternalRoleViewer,
			api.ExternalRoleOperator,
		},
	}

	result := extractor.ExtractOrgRolesFromMap(claims)
	require.NotNil(t, result)

	// flightctl-admin should be filtered out
	globalRoles := result["*"]
	assert.NotContains(t, globalRoles, api.ExternalRoleAdmin, "flightctl-admin should be filtered out")
	assert.Contains(t, globalRoles, api.ExternalRoleViewer, "viewer role should be present")
	assert.Contains(t, globalRoles, api.ExternalRoleOperator, "operator role should be present")
}

func TestRoleExtractor_AllowFlightctlAdmin_CreatedBySuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create role assignment with dynamic role mapping
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
	}
	err := roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	require.NoError(t, err)

	// Create extractor with createdBySuperAdmin=true
	extractor := NewRoleExtractor(roleAssignment, true, log)

	// Test claims with flightctl-admin role
	claims := map[string]interface{}{
		"roles": []interface{}{
			api.ExternalRoleAdmin,
			api.ExternalRoleViewer,
			api.ExternalRoleOperator,
		},
	}

	result := extractor.ExtractOrgRolesFromMap(claims)
	require.NotNil(t, result)

	// flightctl-admin should be allowed
	globalRoles := result["*"]
	assert.Contains(t, globalRoles, api.ExternalRoleAdmin, "flightctl-admin should be allowed when created by super admin")
	assert.Contains(t, globalRoles, api.ExternalRoleViewer, "viewer role should be present")
	assert.Contains(t, globalRoles, api.ExternalRoleOperator, "operator role should be present")
}

func TestRoleExtractor_FilterOrgScopedFlightctlAdmin_NotCreatedBySuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create role assignment with dynamic role mapping
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
		Separator: lo.ToPtr(":"),
	}
	err := roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	require.NoError(t, err)

	// Create extractor with createdBySuperAdmin=false
	extractor := NewRoleExtractor(roleAssignment, false, log)

	// Test claims with org-scoped flightctl-admin role
	claims := map[string]interface{}{
		"roles": []interface{}{
			"org1:" + api.ExternalRoleAdmin,
			"org1:" + api.ExternalRoleViewer,
			"org2:" + api.ExternalRoleAdmin,
			"org2:" + api.ExternalRoleOperator,
		},
	}

	result := extractor.ExtractOrgRolesFromMap(claims)
	require.NotNil(t, result)

	// Org-scoped flightctl-admin should be filtered out
	org1Roles := result["org1"]
	assert.NotContains(t, org1Roles, api.ExternalRoleAdmin, "org1 flightctl-admin should be filtered out")
	assert.Contains(t, org1Roles, api.ExternalRoleViewer, "org1 viewer role should be present")

	org2Roles := result["org2"]
	assert.NotContains(t, org2Roles, api.ExternalRoleAdmin, "org2 flightctl-admin should be filtered out")
	assert.Contains(t, org2Roles, api.ExternalRoleOperator, "org2 operator role should be present")
}

func TestRoleExtractor_AllowOrgScopedFlightctlAdmin_CreatedBySuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create role assignment with dynamic role mapping
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
		Separator: lo.ToPtr(":"),
	}
	err := roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	require.NoError(t, err)

	// Create extractor with createdBySuperAdmin=true
	extractor := NewRoleExtractor(roleAssignment, true, log)

	// Test claims with org-scoped flightctl-admin role
	claims := map[string]interface{}{
		"roles": []interface{}{
			"org1:" + api.ExternalRoleAdmin,
			"org1:" + api.ExternalRoleViewer,
			"org2:" + api.ExternalRoleAdmin,
		},
	}

	result := extractor.ExtractOrgRolesFromMap(claims)
	require.NotNil(t, result)

	// Org-scoped flightctl-admin should be allowed
	org1Roles := result["org1"]
	assert.Contains(t, org1Roles, api.ExternalRoleAdmin, "org1 flightctl-admin should be allowed when created by super admin")
	assert.Contains(t, org1Roles, api.ExternalRoleViewer, "org1 viewer role should be present")

	org2Roles := result["org2"]
	assert.Contains(t, org2Roles, api.ExternalRoleAdmin, "org2 flightctl-admin should be allowed when created by super admin")
}

func TestRoleExtractor_MixedRoles_NotCreatedBySuperAdmin(t *testing.T) {
	log := logrus.New()
	log.SetLevel(logrus.ErrorLevel)

	// Create role assignment with dynamic role mapping
	roleAssignment := api.AuthRoleAssignment{}
	dynamicRoleAssignment := api.AuthDynamicRoleAssignment{
		Type:      api.AuthDynamicRoleAssignmentTypeDynamic,
		ClaimPath: []string{"roles"},
		Separator: lo.ToPtr(":"),
	}
	err := roleAssignment.FromAuthDynamicRoleAssignment(dynamicRoleAssignment)
	require.NoError(t, err)

	// Create extractor with createdBySuperAdmin=false
	extractor := NewRoleExtractor(roleAssignment, false, log)

	// Test claims with mixed global and org-scoped roles including flightctl-admin
	claims := map[string]interface{}{
		"roles": []interface{}{
			api.ExternalRoleAdmin,              // Global flightctl-admin - should be filtered
			api.ExternalRoleViewer,             // Global viewer - should be allowed
			"org1:" + api.ExternalRoleAdmin,    // Org-scoped flightctl-admin - should be filtered
			"org1:" + api.ExternalRoleOperator, // Org-scoped operator - should be allowed
			"org2:" + api.ExternalRoleViewer,   // Org-scoped viewer - should be allowed
		},
	}

	result := extractor.ExtractOrgRolesFromMap(claims)
	require.NotNil(t, result)

	// Check global roles
	globalRoles := result["*"]
	assert.NotContains(t, globalRoles, api.ExternalRoleAdmin, "global flightctl-admin should be filtered out")
	assert.Contains(t, globalRoles, api.ExternalRoleViewer, "global viewer should be present")

	// Check org1 roles
	org1Roles := result["org1"]
	assert.NotContains(t, org1Roles, api.ExternalRoleAdmin, "org1 flightctl-admin should be filtered out")
	assert.Contains(t, org1Roles, api.ExternalRoleOperator, "org1 operator should be present")

	// Check org2 roles
	org2Roles := result["org2"]
	assert.Contains(t, org2Roles, api.ExternalRoleViewer, "org2 viewer should be present")
}
