// Package testdata holds pure test-data builders with no dependency on server/transport
// construction, unlike test/util (whose util.go pulls in internal/api_server, and
// transitively internal/transport/{v1beta1,v1alpha1,agent/v1beta1}). Focused
// internal/service/{resource} sub-package unit tests can import this package directly without
// risking an import cycle back through the transport layer, which now imports every focused
// service package.
package testdata

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/google/uuid"
	"github.com/samber/lo"
)

// ReturnTestAuthProvider creates a test auth provider with the given parameters.
func ReturnTestAuthProvider(orgId uuid.UUID, name string, issuer string, labels *map[string]string) api.AuthProvider {
	if issuer == "" {
		issuer = "https://accounts.google.com"
	}

	// Create organization assignment
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}
	if err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment); err != nil {
		panic(fmt.Sprintf("failed to create organization assignment: %v", err))
	}

	// Create role assignment
	roleAssignment := api.AuthRoleAssignment{}
	staticRoleAssignment := api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{api.ExternalRoleViewer},
	}
	if err := roleAssignment.FromAuthStaticRoleAssignment(staticRoleAssignment); err != nil {
		panic(fmt.Sprintf("failed to create role assignment: %v", err))
	}

	// Create OIDC provider spec
	oidcSpec := api.OIDCProviderSpec{
		ProviderType:           api.Oidc,
		Issuer:                 issuer,
		ClientId:               fmt.Sprintf("test-client-id-%s", name),
		ClientSecret:           "test-client-secret",
		Scopes:                 lo.ToPtr([]string{"openid", "profile", "email"}),
		Enabled:                lo.ToPtr(true),
		UsernameClaim:          lo.ToPtr([]string{"preferred_username"}),
		RoleAssignment:         roleAssignment,
		OrganizationAssignment: assignment,
	}

	authProvider := api.AuthProvider{
		Metadata: api.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: labels,
		},
	}
	if err := authProvider.Spec.FromOIDCProviderSpec(oidcSpec); err != nil {
		panic(fmt.Sprintf("failed to create auth provider spec: %v", err))
	}

	return authProvider
}

// CreateTestOrganizationAssignment returns an AuthOrganizationAssignment pointing at
// "default-org", for tests that don't need ReturnTestAuthProvider's full OIDC provider shape.
func CreateTestOrganizationAssignment() api.AuthOrganizationAssignment {
	assignment := api.AuthOrganizationAssignment{}
	staticAssignment := api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "default-org",
	}
	if err := assignment.FromAuthStaticOrganizationAssignment(staticAssignment); err != nil {
		panic(fmt.Sprintf("failed to create organization assignment: %v", err))
	}
	return assignment
}
