package auth

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitOIDCAuth_NormalizesIssuer(t *testing.T) {
	cfg := config.NewDefault()
	config.WithOIDCAuth("HTTPS://IdP.Example.COM/Realm/", "client", true)(cfg)

	authN, err := initOIDCAuth(cfg, logrus.New())
	require.NoError(t, err)
	oidcAuth, ok := authN.(*authn.OIDCAuth)
	require.True(t, ok)
	assert.Equal(t, "https://idp.example.com/Realm", oidcAuth.GetOIDCSpec().Issuer)
}

func TestInitOAuth2Auth_NormalizesURLs(t *testing.T) {
	cfg := config.NewDefault()
	config.WithOAuth2Auth(
		"HTTPS://IdP.Example.COM/authorize/",
		"HTTPS://IdP.Example.COM/token/",
		"HTTPS://IdP.Example.COM/userinfo/",
		"HTTPS://IdP.Example.COM/",
		"client",
		true,
	)(cfg)
	cfg.Auth.OAuth2.ClientSecret = "secret"
	introspection := &api.OAuth2Introspection{}
	require.NoError(t, introspection.FromRfc7662IntrospectionSpec(api.Rfc7662IntrospectionSpec{
		Type: api.Rfc7662,
		Url:  "https://idp.example.com/introspect",
	}))
	cfg.Auth.OAuth2.Introspection = introspection

	authN, err := initOAuth2Auth(cfg, logrus.New())
	require.NoError(t, err)
	oauth2Auth, ok := authN.(*authn.OAuth2Auth)
	require.True(t, ok)
	got := oauth2Auth.GetOAuth2Spec()
	assert.Equal(t, "https://idp.example.com/authorize", got.AuthorizationUrl)
	assert.Equal(t, "https://idp.example.com/token", got.TokenUrl)
	assert.Equal(t, "https://idp.example.com/userinfo", got.UserinfoUrl)
	require.NotNil(t, got.Issuer)
	assert.Equal(t, "https://idp.example.com", *got.Issuer)
}

func TestInitAAPAuth_NormalizesApiUrl(t *testing.T) {
	cfg := config.NewDefault()
	config.WithAAPAuth("HTTPS://Gateway.Example.COM/", "")(cfg)

	authN, err := initAAPAuth(cfg, logrus.New())
	require.NoError(t, err)
	aapAuth, ok := authN.(*authn.AapGatewayAuth)
	require.True(t, ok)
	assert.Equal(t, "https://gateway.example.com", aapAuth.GetAapSpec().ApiUrl)
}

func TestInitK8sAuth_WhenApiUrlInvalidItShouldFailBeforeClientCreate(t *testing.T) {
	cfg := config.NewDefault()
	config.WithK8sAuth("not-a-url", "default")(cfg)

	_, err := initK8sAuth(cfg, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid K8s ApiUrl")
}

func TestInitOpenShiftAuth_WhenControlPlaneURLInvalidItShouldFailBeforeClientCreate(t *testing.T) {
	cfg := config.NewDefault()
	config.WithOIDCAuth("https://unused.example.com", "unused", false)(cfg)
	cfg.Auth.OIDC = nil
	cfg.Auth.OpenShift = &api.OpenShiftProviderSpec{
		ProviderType:           api.Openshift,
		ClusterControlPlaneUrl: lo.ToPtr("not-a-url"),
		AuthorizationUrl:       lo.ToPtr("https://oauth.example.com/oauth/authorize"),
		ClientId:               lo.ToPtr("console"),
		Enabled:                lo.ToPtr(true),
	}

	_, err := initOpenShiftAuth(cfg, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid OpenShift ClusterControlPlaneUrl")
}

func TestInitOpenShiftAuth_WhenAuthorizationURLInvalidItShouldFail(t *testing.T) {
	cfg := config.NewDefault()
	config.WithOIDCAuth("https://unused.example.com", "unused", false)(cfg)
	cfg.Auth.OIDC = nil
	cfg.Auth.OpenShift = &api.OpenShiftProviderSpec{
		ProviderType:           api.Openshift,
		ClusterControlPlaneUrl: lo.ToPtr("https://api.example.com:6443"),
		AuthorizationUrl:       lo.ToPtr("not-a-url"),
		ClientId:               lo.ToPtr("console"),
		Enabled:                lo.ToPtr(true),
	}

	_, err := initOpenShiftAuth(cfg, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid OpenShift AuthorizationUrl")
}

func TestInitAAPAuth_WhenApiUrlInvalidItShouldFail(t *testing.T) {
	cfg := config.NewDefault()
	config.WithAAPAuth("not-a-url", "")(cfg)

	_, err := initAAPAuth(cfg, logrus.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid AAP ApiUrl")
}
