package provider

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeOIDCProviderSpecURLs(t *testing.T) {
	t.Run("When issuer has mixed case and trailing slash it should canonicalize", func(t *testing.T) {
		spec := api.OIDCProviderSpec{
			ProviderType: api.Oidc,
			Issuer:       "HTTPS://IdP.Example.COM/Realm/",
			ClientId:     "client",
		}
		require.NoError(t, NormalizeOIDCProviderSpecURLs(&spec))
		assert.Equal(t, "https://idp.example.com/Realm", spec.Issuer)
	})

	t.Run("When issuer is empty it should be a no-op", func(t *testing.T) {
		spec := api.OIDCProviderSpec{ProviderType: api.Oidc, ClientId: "client"}
		require.NoError(t, NormalizeOIDCProviderSpecURLs(&spec))
		assert.Empty(t, spec.Issuer)
	})

	t.Run("When issuer is invalid it should fail", func(t *testing.T) {
		spec := api.OIDCProviderSpec{ProviderType: api.Oidc, Issuer: "not-a-url", ClientId: "client"}
		err := NormalizeOIDCProviderSpecURLs(&spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid OIDC issuer URL")
	})
}

func TestNormalizeOAuth2ProviderSpecURLs(t *testing.T) {
	t.Run("When URL fields have mixed case and trailing slashes they should canonicalize", func(t *testing.T) {
		spec := api.OAuth2ProviderSpec{
			ProviderType:     api.Oauth2,
			AuthorizationUrl: "HTTPS://IdP.Example.COM/oauth2/authorize/",
			TokenUrl:         "HTTPS://IdP.Example.COM/token/",
			UserinfoUrl:      "HTTPS://IdP.Example.COM/userinfo/",
			Issuer:           lo.ToPtr("HTTPS://IdP.Example.COM/"),
			ClientId:         "client",
		}
		require.NoError(t, NormalizeOAuth2ProviderSpecURLs(&spec))
		assert.Equal(t, "https://idp.example.com/oauth2/authorize", spec.AuthorizationUrl)
		assert.Equal(t, "https://idp.example.com/token", spec.TokenUrl)
		assert.Equal(t, "https://idp.example.com/userinfo", spec.UserinfoUrl)
		require.NotNil(t, spec.Issuer)
		assert.Equal(t, "https://idp.example.com", *spec.Issuer)
	})

	t.Run("When userinfoUrl is invalid it should fail", func(t *testing.T) {
		spec := api.OAuth2ProviderSpec{
			ProviderType:     api.Oauth2,
			AuthorizationUrl: "https://idp.example.com/authorize",
			TokenUrl:         "https://idp.example.com/token",
			UserinfoUrl:      "not-a-url",
			ClientId:         "client",
		}
		err := NormalizeOAuth2ProviderSpecURLs(&spec)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid OAuth2 userinfoUrl")
	})
}

func TestNormalizeAuthProviderSpecURLs(t *testing.T) {
	t.Run("When OIDC union it should normalize issuer", func(t *testing.T) {
		spec := api.AuthProviderSpec{}
		require.NoError(t, spec.FromOIDCProviderSpec(api.OIDCProviderSpec{
			ProviderType: api.Oidc,
			Issuer:       "HTTPS://IdP.Example.COM/",
			ClientId:     "client",
		}))
		require.NoError(t, NormalizeAuthProviderSpecURLs(&spec))
		got, err := spec.AsOIDCProviderSpec()
		require.NoError(t, err)
		assert.Equal(t, "https://idp.example.com", got.Issuer)
	})

	t.Run("When OAuth2 union it should normalize uniqueness fields", func(t *testing.T) {
		introspection := &api.OAuth2Introspection{}
		require.NoError(t, introspection.FromRfc7662IntrospectionSpec(api.Rfc7662IntrospectionSpec{
			Type: api.Rfc7662,
			Url:  "https://idp.example.com/introspect",
		}))
		spec := api.AuthProviderSpec{}
		require.NoError(t, spec.FromOAuth2ProviderSpec(api.OAuth2ProviderSpec{
			ProviderType:     api.Oauth2,
			AuthorizationUrl: "HTTPS://IdP.Example.COM/authorize/",
			TokenUrl:         "HTTPS://IdP.Example.COM/token/",
			UserinfoUrl:      "HTTPS://IdP.Example.COM/userinfo/",
			ClientId:         "client",
			Introspection:    introspection,
		}))
		require.NoError(t, NormalizeAuthProviderSpecURLs(&spec))
		got, err := spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		assert.Equal(t, "https://idp.example.com/authorize", got.AuthorizationUrl)
		assert.Equal(t, "https://idp.example.com/token", got.TokenUrl)
		assert.Equal(t, "https://idp.example.com/userinfo", got.UserinfoUrl)
	})

	t.Run("When AAP union it should leave ApiUrl unchanged", func(t *testing.T) {
		spec := api.AuthProviderSpec{}
		require.NoError(t, spec.FromAapProviderSpec(api.AapProviderSpec{
			ProviderType:     api.Aap,
			ApiUrl:           "HTTPS://AAP.Example.COM/",
			AuthorizationUrl: "https://aap.example.com/authorize",
			TokenUrl:         "https://aap.example.com/token",
			ClientId:         "client",
			ClientSecret:     "secret",
			Scopes:           []string{"api"},
		}))
		require.NoError(t, NormalizeAuthProviderSpecURLs(&spec))
		got, err := spec.AsAapProviderSpec()
		require.NoError(t, err)
		assert.Equal(t, "HTTPS://AAP.Example.COM/", got.ApiUrl)
	})
}
