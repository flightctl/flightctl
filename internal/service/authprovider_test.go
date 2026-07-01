package service

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeAuthProviderURLs_OIDC(t *testing.T) {
	tests := []struct {
		name        string
		issuer      string
		wantIssuer  string
		wantErr     bool
		errContains string
	}{
		{
			name:       "When OIDC issuer has no trailing slash it should stay unchanged",
			issuer:     "https://idp.example.com",
			wantIssuer: "https://idp.example.com",
		},
		{
			name:       "When OIDC issuer has trailing slash it should be stripped",
			issuer:     "https://idp.example.com/",
			wantIssuer: "https://idp.example.com",
		},
		{
			name:       "When OIDC issuer has path and trailing slash it should strip only trailing slash",
			issuer:     "https://idp.example.com/realm/master/",
			wantIssuer: "https://idp.example.com/realm/master",
		},
		{
			name:       "When OIDC issuer has port it should be preserved",
			issuer:     "https://idp.example.com:8443/",
			wantIssuer: "https://idp.example.com:8443",
		},
		{
			name:        "When OIDC issuer has no scheme it should fail",
			issuer:      "idp.example.com",
			wantErr:     true,
			errContains: "invalid OIDC issuer URL",
		},
		{
			name:       "When OIDC issuer is empty it should be a no-op (validation catches it separately)",
			issuer:     "",
			wantIssuer: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := domain.AuthProviderSpec{}
			oidcSpec := domain.OIDCProviderSpec{
				ProviderType: domain.Oidc,
				Issuer:       tt.issuer,
				ClientId:     "test-client",
			}
			require.NoError(t, spec.FromOIDCProviderSpec(oidcSpec))

			err := normalizeAuthProviderURLs(&spec)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			got, err := spec.AsOIDCProviderSpec()
			require.NoError(t, err)
			assert.Equal(t, tt.wantIssuer, got.Issuer)
		})
	}
}

func TestNormalizeAuthProviderURLs_OAuth2(t *testing.T) {
	tests := []struct {
		name             string
		authorizationUrl string
		issuer           *string
		wantAuthzURL     string
		wantIssuer       *string
		wantErr          bool
		errContains      string
	}{
		{
			name:             "When authorizationUrl has trailing slash it should be stripped",
			authorizationUrl: "https://idp.example.com/oauth2/authorize/",
			wantAuthzURL:     "https://idp.example.com/oauth2/authorize",
		},
		{
			name:             "When both authorizationUrl and issuer have trailing slashes they should both be stripped",
			authorizationUrl: "https://idp.example.com/oauth2/authorize/",
			issuer:           lo.ToPtr("https://idp.example.com/"),
			wantAuthzURL:     "https://idp.example.com/oauth2/authorize",
			wantIssuer:       lo.ToPtr("https://idp.example.com"),
		},
		{
			name:             "When issuer is nil it should be left nil",
			authorizationUrl: "https://idp.example.com/oauth2/authorize",
			issuer:           nil,
			wantAuthzURL:     "https://idp.example.com/oauth2/authorize",
			wantIssuer:       nil,
		},
		{
			name:             "When authorizationUrl is invalid it should fail",
			authorizationUrl: "not-a-url",
			wantErr:          true,
			errContains:      "invalid OAuth2 authorizationUrl",
		},
		{
			name:             "When issuer is invalid it should fail",
			authorizationUrl: "https://idp.example.com/authorize",
			issuer:           lo.ToPtr("not-a-url"),
			wantErr:          true,
			errContains:      "invalid OAuth2 issuer URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rfc7662 := &domain.OAuth2Introspection{}
			require.NoError(t, rfc7662.FromRfc7662IntrospectionSpec(domain.Rfc7662IntrospectionSpec{
				Type: domain.IntrospectionTypeRfc7662,
				Url:  "https://idp.example.com/introspect",
			}))
			spec := domain.AuthProviderSpec{}
			oauth2Spec := domain.OAuth2ProviderSpec{
				ProviderType:     domain.Oauth2,
				AuthorizationUrl: tt.authorizationUrl,
				TokenUrl:         "https://idp.example.com/token",
				UserinfoUrl:      "https://idp.example.com/userinfo",
				ClientId:         "test-client",
				Issuer:           tt.issuer,
				Introspection:    rfc7662,
			}
			require.NoError(t, spec.FromOAuth2ProviderSpec(oauth2Spec))

			err := normalizeAuthProviderURLs(&spec)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			got, err := spec.AsOAuth2ProviderSpec()
			require.NoError(t, err)
			assert.Equal(t, tt.wantAuthzURL, got.AuthorizationUrl)
			if tt.wantIssuer == nil {
				assert.Nil(t, got.Issuer)
			} else {
				require.NotNil(t, got.Issuer)
				assert.Equal(t, *tt.wantIssuer, *got.Issuer)
			}
		})
	}
}

func TestNormalizeAuthProviderURLs_NonURLTypes(t *testing.T) {
	t.Run("When AAP provider type it should be a no-op", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromAapProviderSpec(domain.AapProviderSpec{
			ProviderType:     domain.Aap,
			ApiUrl:           "https://aap.example.com/",
			AuthorizationUrl: "https://aap.example.com/authorize",
			TokenUrl:         "https://aap.example.com/token",
			ClientId:         "client",
			ClientSecret:     "secret",
			Scopes:           []string{"api"},
		}))
		err := normalizeAuthProviderURLs(&spec)
		require.NoError(t, err)
		got, err := spec.AsAapProviderSpec()
		require.NoError(t, err)
		// AAP ApiUrl is not touched by normalizeAuthProviderURLs
		assert.Equal(t, "https://aap.example.com/", got.ApiUrl)
	})
}

func TestApplyAuthProviderDefaults_OIDC(t *testing.T) {
	t.Run("When UsernameClaim is nil it should default to preferred_username", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
			ProviderType: domain.Oidc,
			Issuer:       "https://idp.example.com",
			ClientId:     "test-client",
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOIDCProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.UsernameClaim)
		assert.Equal(t, []string{"preferred_username"}, *got.UsernameClaim)
	})

	t.Run("When UsernameClaim is already set it should not be overwritten", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOIDCProviderSpec(domain.OIDCProviderSpec{
			ProviderType:  domain.Oidc,
			Issuer:        "https://idp.example.com",
			ClientId:      "test-client",
			UsernameClaim: lo.ToPtr([]string{"email"}),
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOIDCProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.UsernameClaim)
		assert.Equal(t, []string{"email"}, *got.UsernameClaim)
	})
}

func TestApplyAuthProviderDefaults_OAuth2(t *testing.T) {
	rfc7662 := func(t *testing.T) *domain.OAuth2Introspection {
		t.Helper()
		i := &domain.OAuth2Introspection{}
		require.NoError(t, i.FromRfc7662IntrospectionSpec(domain.Rfc7662IntrospectionSpec{
			Type: domain.IntrospectionTypeRfc7662,
			Url:  "https://idp.example.com/introspect",
		}))
		return i
	}

	t.Run("When Issuer is nil it should be set to AuthorizationUrl", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOAuth2ProviderSpec(domain.OAuth2ProviderSpec{
			ProviderType:     domain.Oauth2,
			AuthorizationUrl: "https://idp.example.com/authorize",
			TokenUrl:         "https://idp.example.com/token",
			UserinfoUrl:      "https://idp.example.com/userinfo",
			ClientId:         "test-client",
			Introspection:    rfc7662(t),
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.Issuer)
		assert.Equal(t, "https://idp.example.com/authorize", *got.Issuer)
	})

	t.Run("When Issuer is already set it should not be overwritten", func(t *testing.T) {
		explicitIssuer := "https://idp.example.com"
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOAuth2ProviderSpec(domain.OAuth2ProviderSpec{
			ProviderType:     domain.Oauth2,
			AuthorizationUrl: "https://idp.example.com/authorize",
			TokenUrl:         "https://idp.example.com/token",
			UserinfoUrl:      "https://idp.example.com/userinfo",
			ClientId:         "test-client",
			Issuer:           &explicitIssuer,
			Introspection:    rfc7662(t),
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.Issuer)
		assert.Equal(t, explicitIssuer, *got.Issuer)
	})

	t.Run("When Introspection is nil and token URL is a GitHub URL it should infer GitHub introspection", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOAuth2ProviderSpec(domain.OAuth2ProviderSpec{
			ProviderType:     domain.Oauth2,
			AuthorizationUrl: "https://github.com/login/oauth/authorize",
			TokenUrl:         "https://github.com/login/oauth/access_token",
			UserinfoUrl:      "https://api.github.com/user",
			ClientId:         "test-client",
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.Introspection)
		discriminator, err := got.Introspection.Discriminator()
		require.NoError(t, err)
		assert.Equal(t, string(domain.IntrospectionTypeGithub), discriminator)
	})

	t.Run("When Introspection is nil and token URL is a standard URL it should infer RFC 7662 introspection", func(t *testing.T) {
		spec := domain.AuthProviderSpec{}
		require.NoError(t, spec.FromOAuth2ProviderSpec(domain.OAuth2ProviderSpec{
			ProviderType:     domain.Oauth2,
			AuthorizationUrl: "https://idp.example.com/authorize",
			TokenUrl:         "https://idp.example.com/oauth2/token",
			UserinfoUrl:      "https://idp.example.com/userinfo",
			ClientId:         "test-client",
		}))
		require.NoError(t, applyAuthProviderDefaults(&spec))
		got, err := spec.AsOAuth2ProviderSpec()
		require.NoError(t, err)
		require.NotNil(t, got.Introspection)
		discriminator, err := got.Introspection.Discriminator()
		require.NoError(t, err)
		assert.Equal(t, string(domain.IntrospectionTypeRfc7662), discriminator)
	})
}

func TestSanitizeSchemaError(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		wantMsg string
	}{
		{
			name:    "When nil error it should return empty string",
			err:     nil,
			wantMsg: "",
		},
		{
			name:    "When non-sensitive error it should return original message",
			err:     assert.AnError,
			wantMsg: assert.AnError.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeSchemaError(tt.err)
			assert.Equal(t, tt.wantMsg, got)
		})
	}
}
