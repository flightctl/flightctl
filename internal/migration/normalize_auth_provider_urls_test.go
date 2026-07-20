package migration

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordNormalizedAuthProviderKey_DetectsOIDCCollision(t *testing.T) {
	seenOIDC := make(map[oidcUniquenessKey]string)
	seenOAuth2 := make(map[oauth2UniquenessKey]string)

	specA := domain.AuthProviderSpec{}
	require.NoError(t, specA.FromOIDCProviderSpec(api.OIDCProviderSpec{
		ProviderType: api.Oidc,
		Issuer:       "https://idp.example.com",
		ClientId:     "client",
	}))
	require.NoError(t, recordNormalizedAuthProviderKey("provider-a", specA, seenOIDC, seenOAuth2))

	specB := domain.AuthProviderSpec{}
	require.NoError(t, specB.FromOIDCProviderSpec(api.OIDCProviderSpec{
		ProviderType: api.Oidc,
		Issuer:       "https://idp.example.com",
		ClientId:     "client",
	}))
	err := recordNormalizedAuthProviderKey("provider-b", specB, seenOIDC, seenOAuth2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would share issuer")
	assert.Contains(t, err.Error(), "provider-a")
	assert.Contains(t, err.Error(), "provider-b")
}

func TestRecordNormalizedAuthProviderKey_DetectsOAuth2Collision(t *testing.T) {
	seenOIDC := make(map[oidcUniquenessKey]string)
	seenOAuth2 := make(map[oauth2UniquenessKey]string)

	introspection := &api.OAuth2Introspection{}
	require.NoError(t, introspection.FromRfc7662IntrospectionSpec(api.Rfc7662IntrospectionSpec{
		Type: api.Rfc7662,
		Url:  "https://idp.example.com/introspect",
	}))

	specA := domain.AuthProviderSpec{}
	require.NoError(t, specA.FromOAuth2ProviderSpec(api.OAuth2ProviderSpec{
		ProviderType:     api.Oauth2,
		AuthorizationUrl: "https://idp.example.com/authorize",
		TokenUrl:         "https://idp.example.com/token",
		UserinfoUrl:      "https://idp.example.com/userinfo",
		ClientId:         "client",
		Introspection:    introspection,
	}))
	require.NoError(t, recordNormalizedAuthProviderKey("oauth2-a", specA, seenOIDC, seenOAuth2))

	specB := domain.AuthProviderSpec{}
	require.NoError(t, specB.FromOAuth2ProviderSpec(api.OAuth2ProviderSpec{
		ProviderType:     api.Oauth2,
		AuthorizationUrl: "https://other.example.com/authorize",
		TokenUrl:         "https://other.example.com/token",
		UserinfoUrl:      "https://idp.example.com/userinfo",
		ClientId:         "client",
		Introspection:    introspection,
	}))
	err := recordNormalizedAuthProviderKey("oauth2-b", specB, seenOIDC, seenOAuth2)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "would share userinfoUrl")
}

func TestCloneAuthProviderSpec_IsIndependent(t *testing.T) {
	original := domain.AuthProviderSpec{}
	require.NoError(t, original.FromOIDCProviderSpec(api.OIDCProviderSpec{
		ProviderType: api.Oidc,
		Issuer:       "https://idp.example.com/",
		ClientId:     "client",
	}))

	cloned, err := cloneAuthProviderSpec(original)
	require.NoError(t, err)

	oidc, err := cloned.AsOIDCProviderSpec()
	require.NoError(t, err)
	oidc.Issuer = "https://changed.example.com"
	require.NoError(t, cloned.MergeOIDCProviderSpec(oidc))

	origOIDC, err := original.AsOIDCProviderSpec()
	require.NoError(t, err)
	assert.Equal(t, "https://idp.example.com/", origOIDC.Issuer)
}
