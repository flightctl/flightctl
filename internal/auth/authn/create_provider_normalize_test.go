package authn

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateOIDCAuthFromProvider_NormalizesIssuer(t *testing.T) {
	name := "mixed-case-oidc"
	enabled := true
	provider := &api.AuthProvider{
		Metadata: api.ObjectMeta{Name: &name},
	}
	require.NoError(t, provider.Spec.FromOIDCProviderSpec(api.OIDCProviderSpec{
		ProviderType: api.Oidc,
		Issuer:       "HTTPS://IdP.Example.COM/Realm/",
		ClientId:     "client",
		Enabled:      &enabled,
	}))

	authN, err := createOIDCAuthFromProvider(provider, nil, logrus.New())
	require.NoError(t, err)
	oidcAuth, ok := authN.(*OIDCAuth)
	require.True(t, ok)
	assert.Equal(t, "https://idp.example.com/realm", oidcAuth.GetOIDCSpec().Issuer)
}

func TestCreateOAuth2AuthFromProvider_NormalizesURLs(t *testing.T) {
	name := "mixed-case-oauth2"
	enabled := true
	introspection := &api.OAuth2Introspection{}
	require.NoError(t, introspection.FromRfc7662IntrospectionSpec(api.Rfc7662IntrospectionSpec{
		Type: api.Rfc7662,
		Url:  "https://idp.example.com/introspect",
	}))
	provider := &api.AuthProvider{
		Metadata: api.ObjectMeta{Name: &name},
	}
	require.NoError(t, provider.Spec.FromOAuth2ProviderSpec(api.OAuth2ProviderSpec{
		ProviderType:     api.Oauth2,
		AuthorizationUrl: "HTTPS://IdP.Example.COM/oauth2/authorize/",
		TokenUrl:         "HTTPS://IdP.Example.COM/token/",
		UserinfoUrl:      "HTTPS://IdP.Example.COM/userinfo/",
		Issuer:           lo.ToPtr("HTTPS://IdP.Example.COM/"),
		ClientId:         "client",
		ClientSecret:     "secret",
		Enabled:          &enabled,
		Introspection:    introspection,
	}))

	authN, err := createOAuth2AuthFromProvider(context.Background(), provider, nil, logrus.New())
	require.NoError(t, err)
	oauth2Auth, ok := authN.(*OAuth2Auth)
	require.True(t, ok)
	got := oauth2Auth.GetOAuth2Spec()
	assert.Equal(t, "https://idp.example.com/oauth2/authorize", got.AuthorizationUrl)
	assert.Equal(t, "https://idp.example.com/token", got.TokenUrl)
	assert.Equal(t, "https://idp.example.com/userinfo", got.UserinfoUrl)
	require.NotNil(t, got.Issuer)
	assert.Equal(t, "https://idp.example.com", *got.Issuer)
}

func TestCreateOIDCAuthFromProvider_WhenIssuerInvalidItShouldFail(t *testing.T) {
	name := "bad-oidc"
	provider := &api.AuthProvider{
		Metadata: api.ObjectMeta{Name: &name},
	}
	require.NoError(t, provider.Spec.FromOIDCProviderSpec(api.OIDCProviderSpec{
		ProviderType: api.Oidc,
		Issuer:       "not-a-url",
		ClientId:     "client",
	}))

	_, err := createOIDCAuthFromProvider(provider, nil, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to normalize OIDC provider URLs")
}
