package authn

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubOAuth2Middleware struct {
	MockAuthNMiddleware
	spec api.OAuth2ProviderSpec
}

func (s *stubOAuth2Middleware) GetOAuth2Spec() api.OAuth2ProviderSpec {
	return s.spec
}

func oauth2SpecForChangeCompare(t *testing.T, authorizationURL, tokenURL, userinfoURL string) api.OAuth2ProviderSpec {
	t.Helper()

	introspection := &api.OAuth2Introspection{}
	require.NoError(t, introspection.FromRfc7662IntrospectionSpec(api.Rfc7662IntrospectionSpec{
		Type: api.Rfc7662,
		Url:  "https://idp.example.com/introspect",
	}))

	orgAssignment := api.AuthOrganizationAssignment{}
	require.NoError(t, orgAssignment.FromAuthStaticOrganizationAssignment(api.AuthStaticOrganizationAssignment{
		Type:             api.AuthStaticOrganizationAssignmentTypeStatic,
		OrganizationName: "test-org",
	}))

	roleAssignment := api.AuthRoleAssignment{}
	require.NoError(t, roleAssignment.FromAuthStaticRoleAssignment(api.AuthStaticRoleAssignment{
		Type:  api.AuthStaticRoleAssignmentTypeStatic,
		Roles: []string{"viewer"},
	}))

	return api.OAuth2ProviderSpec{
		ProviderType:           api.Oauth2,
		AuthorizationUrl:       authorizationURL,
		TokenUrl:               tokenURL,
		UserinfoUrl:            userinfoURL,
		ClientId:               "client",
		ClientSecret:           "secret",
		Introspection:          introspection,
		OrganizationAssignment: orgAssignment,
		RoleAssignment:         roleAssignment,
	}
}

func TestHasProviderChanged_OAuth2URLNormalizationEquivalence(t *testing.T) {
	existing := oauth2SpecForChangeCompare(t,
		"https://idp.example.com/authorize",
		"https://idp.example.com/token",
		"https://idp.example.com/userinfo",
	)
	incoming := oauth2SpecForChangeCompare(t,
		"HTTPS://IdP.Example.COM/authorize/",
		"HTTPS://IdP.Example.COM/token/",
		"HTTPS://IdP.Example.COM/userinfo/",
	)

	provider := &api.AuthProvider{}
	require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(incoming))

	m := NewMultiAuth(nil, nil, logrus.New())
	changed, err := m.hasProviderChanged(&stubOAuth2Middleware{spec: existing}, provider, "oauth2")
	require.NoError(t, err)
	assert.False(t, changed, "host case and trailing-slash variants should not count as a provider change")
}

func TestHasProviderChanged_OAuth2TokenURLPathCaseIsSignificant(t *testing.T) {
	existing := oauth2SpecForChangeCompare(t,
		"https://idp.example.com/authorize",
		"https://idp.example.com/token",
		"https://idp.example.com/userinfo",
	)
	incoming := oauth2SpecForChangeCompare(t,
		"https://idp.example.com/authorize",
		"https://idp.example.com/TOKEN",
		"https://idp.example.com/userinfo",
	)

	provider := &api.AuthProvider{}
	require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(incoming))

	m := NewMultiAuth(nil, nil, logrus.New())
	changed, err := m.hasProviderChanged(&stubOAuth2Middleware{spec: existing}, provider, "oauth2")
	require.NoError(t, err)
	assert.True(t, changed, "tokenUrl path case differences should count as a provider change")
}
