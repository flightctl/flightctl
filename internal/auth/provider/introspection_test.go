package provider

import (
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildIntrospectionURL(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    string
	}{
		{
			name:    "When token URL is provided it should append /introspect",
			baseURL: "https://idp.example.com/oauth2/token",
			want:    "https://idp.example.com/oauth2/introspect",
		},
		{
			name:    "When base URL without /token it should append /introspect",
			baseURL: "https://idp.example.com/oauth2",
			want:    "https://idp.example.com/oauth2/introspect",
		},
		{
			name:    "When base URL has trailing slash it should strip and append /introspect",
			baseURL: "https://idp.example.com/oauth2/token/",
			want:    "https://idp.example.com/oauth2/introspect",
		},
		{
			name:    "When URL has query parameters they should be preserved",
			baseURL: "https://idp.example.com/oauth2/token?realm=master",
			want:    "https://idp.example.com/oauth2/introspect?realm=master",
		},
		{
			name:    "When URL has port it should be preserved",
			baseURL: "https://idp.example.com:8080/oauth2/token",
			want:    "https://idp.example.com:8080/oauth2/introspect",
		},
		{
			name:    "When URL has no scheme or host it should return empty string",
			baseURL: "not-a-url",
			want:    "",
		},
		{
			name:    "When empty string it should return empty string",
			baseURL: "",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildIntrospectionURL(tt.baseURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractGitHubEnterpriseBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		authURL string
		want    string
	}{
		{
			name:    "When GHE authorization URL it should return API base URL",
			authURL: "https://github.enterprise.com/login/oauth/authorize",
			want:    "https://github.enterprise.com/api/v3",
		},
		{
			name:    "When URL has no path it should return empty string",
			authURL: "https://github.enterprise.com",
			want:    "",
		},
		{
			name:    "When URL has no scheme it should return empty string",
			authURL: "github.enterprise.com/login/oauth/authorize",
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractGitHubEnterpriseBaseURL(tt.authURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestInferOAuth2IntrospectionConfig(t *testing.T) {
	tests := []struct {
		name            string
		spec            api.OAuth2ProviderSpec
		wantType        string
		wantErrContains string
	}{
		{
			name: "When GitHub.com URLs it should infer GitHub introspection without custom URL",
			spec: api.OAuth2ProviderSpec{
				AuthorizationUrl: "https://github.com/login/oauth/authorize",
				TokenUrl:         "https://github.com/login/oauth/access_token",
				UserinfoUrl:      "https://api.github.com/user",
				ClientId:         "client-id",
			},
			wantType: string(api.Github),
		},
		{
			name: "When GitHub Enterprise URLs it should infer GitHub introspection with custom URL",
			spec: api.OAuth2ProviderSpec{
				AuthorizationUrl: "https://github.enterprise.com/login/oauth/authorize",
				TokenUrl:         "https://github.enterprise.com/login/oauth/access_token",
				UserinfoUrl:      "https://github.enterprise.com/api/v3/user",
				ClientId:         "client-id",
			},
			wantType: string(api.Github),
		},
		{
			name: "When token URL ends with /token it should infer RFC 7662 with /introspect sibling",
			spec: api.OAuth2ProviderSpec{
				AuthorizationUrl: "https://idp.example.com/oauth2/authorize",
				TokenUrl:         "https://idp.example.com/oauth2/token",
				UserinfoUrl:      "https://idp.example.com/oauth2/userinfo",
				ClientId:         "client-id",
			},
			wantType: string(api.Rfc7662),
		},
		{
			name: "When token URL has no /token suffix it should infer RFC 7662 by appending /introspect",
			spec: api.OAuth2ProviderSpec{
				AuthorizationUrl: "https://idp.example.com/authorize",
				TokenUrl:         "https://idp.example.com/oauth2",
				UserinfoUrl:      "https://idp.example.com/userinfo",
				ClientId:         "client-id",
			},
			wantType: string(api.Rfc7662),
		},
		{
			name: "When token URL is invalid it should fail to infer and return error",
			spec: api.OAuth2ProviderSpec{
				AuthorizationUrl: "not-a-url",
				TokenUrl:         "not-a-url-either",
				UserinfoUrl:      "https://idp.example.com/userinfo",
				ClientId:         "client-id",
			},
			wantErrContains: "could not infer introspection",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InferOAuth2IntrospectionConfig(tt.spec)
			if tt.wantErrContains != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrContains)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, got)
			discriminator, err := got.Discriminator()
			require.NoError(t, err)
			assert.Equal(t, tt.wantType, discriminator)
		})
	}
}

func TestInferRFC7662IntrospectionURL(t *testing.T) {
	tests := []struct {
		name string
		spec api.OAuth2ProviderSpec
		want string
	}{
		{
			name: "When token URL provided it should derive introspection from token URL",
			spec: api.OAuth2ProviderSpec{
				TokenUrl: "https://idp.example.com/oauth2/token",
			},
			want: "https://idp.example.com/oauth2/introspect",
		},
		{
			name: "When only issuer provided it should derive introspection from issuer",
			spec: api.OAuth2ProviderSpec{
				Issuer: lo.ToPtr("https://idp.example.com/oauth2"),
			},
			want: "https://idp.example.com/oauth2/introspect",
		},
		{
			name: "When token URL is invalid and issuer is valid it should fall back to issuer",
			spec: api.OAuth2ProviderSpec{
				TokenUrl: "not-a-url",
				Issuer:   lo.ToPtr("https://idp.example.com/oauth2"),
			},
			want: "https://idp.example.com/oauth2/introspect",
		},
		{
			name: "When both token URL and issuer are invalid it should return empty string",
			spec: api.OAuth2ProviderSpec{
				TokenUrl: "not-a-url",
				Issuer:   lo.ToPtr("also-not-a-url"),
			},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferRFC7662IntrospectionURL(tt.spec)
			assert.Equal(t, tt.want, got)
		})
	}
}
