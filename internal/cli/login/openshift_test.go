package login

import (
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/openshift/osincli"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenShiftOAuthCallback(t *testing.T) {
	tests := []struct {
		name       string
		clientType *api.OpenShiftProviderSpecClientType
		callback   string
		want       string
	}{
		{
			name:     "When the client type is omitted it should preserve the localhost callback",
			callback: "http://localhost:8080/callback",
			want:     "http://localhost:8080/callback",
		},
		{
			name:       "When using an OAuthClient it should preserve the localhost callback",
			clientType: lo.ToPtr(api.Oauthclient),
			callback:   "http://localhost:8080/callback",
			want:       "http://localhost:8080/callback",
		},
		{
			name:       "When using a ServiceAccount client it should use the numeric loopback callback",
			clientType: lo.ToPtr(api.Serviceaccount),
			callback:   "http://localhost:8080/callback",
			want:       "http://127.0.0.1:8080/callback",
		},
		{
			name:       "When using a custom callback port it should preserve it for ServiceAccount clients",
			clientType: lo.ToPtr(api.Serviceaccount),
			callback:   "http://localhost:18080/callback",
			want:       "http://127.0.0.1:18080/callback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPost, r.Method)
				assert.Equal(t, tt.want, r.FormValue("redirect_uri"))
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"access_token":"user-token","token_type":"Bearer","expires_in":300}`))
			}))
			t.Cleanup(server.Close)
			provider := &OpenShift{
				Metadata: api.ObjectMeta{Name: lo.ToPtr("openshift")},
				Spec: api.OpenShiftProviderSpec{
					ClientType:       tt.clientType,
					ClientId:         lo.ToPtr("flightctl"),
					AuthorizationUrl: lo.ToPtr("https://oauth.example.com/authorize"),
				},
				ApiServerURL: server.URL,
			}

			client, err := provider.getOAuth2Client(tt.callback)
			require.NoError(t, err)
			authRequest := client.NewAuthorizeRequest(osincli.CODE)
			authorizeURL := authRequest.GetAuthorizeUrlWithParams("state")
			require.Equal(t, tt.want, authorizeURL.Query().Get("redirect_uri"))

			callbackRequest := httptest.NewRequest(http.MethodGet, tt.want+"?code=test-code", nil)
			token, err := getOAuth2AccessToken(client, authRequest, callbackRequest)
			require.NoError(t, err)
			require.Equal(t, "user-token", token.AccessToken)
		})
	}
}

func TestOpenShiftGetOAuth2ClientScopes(t *testing.T) {
	tests := []struct {
		name   string
		scopes *[]string
		want   string
	}{
		{
			name:   "When scopes are configured it should request them",
			scopes: &[]string{"user:info", "user:check-access", "user:list-projects"},
			want:   "user:info user:check-access user:list-projects",
		},
		{
			name: "When scopes are omitted it should default to user:full",
			want: "user:full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &OpenShift{
				Metadata: api.ObjectMeta{Name: lo.ToPtr("openshift")},
				Spec: api.OpenShiftProviderSpec{
					ClientId:         lo.ToPtr("system:serviceaccount:flightctl:flightctl-api"),
					AuthorizationUrl: lo.ToPtr("https://oauth.example.com/authorize"),
					TokenUrl:         lo.ToPtr("https://oauth.example.com/token"),
					Scopes:           tt.scopes,
				},
				ApiServerURL: "https://api.example.com",
			}

			client, err := provider.getOAuth2Client("http://127.0.0.1:8080/callback")
			require.NoError(t, err)
			authorizeURL := client.NewAuthorizeRequest(osincli.CODE).GetAuthorizeUrlWithParams("state")
			require.Equal(t, tt.want, authorizeURL.Query().Get("scope"))
		})
	}
}

func TestOpenShiftServiceAccountClientRejectsPasswordFlow(t *testing.T) {
	provider := &OpenShift{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("openshift")},
		Spec: api.OpenShiftProviderSpec{
			ClientType:       lo.ToPtr(api.Serviceaccount),
			ClientId:         lo.ToPtr("system:serviceaccount:flightctl:flightctl-api"),
			AuthorizationUrl: lo.ToPtr("https://oauth.example.com/authorize"),
		},
		ApiServerURL: "https://api.example.com",
		Username:     "user",
		Password:     "password",
		Web:          false,
	}

	_, err := provider.Auth()
	require.ErrorContains(t, err, "service-account OAuth clients require browser login")
}

func TestOpenShiftServiceAccountClientValidateRequiresWeb(t *testing.T) {
	provider := &OpenShift{
		Metadata: api.ObjectMeta{Name: lo.ToPtr("openshift")},
		Spec: api.OpenShiftProviderSpec{
			ClientType:       lo.ToPtr(api.Serviceaccount),
			ClientId:         lo.ToPtr("system:serviceaccount:flightctl:flightctl-api"),
			AuthorizationUrl: lo.ToPtr("https://oauth.example.com/authorize"),
		},
		ApiServerURL: "https://api.example.com",
		Web:          false,
	}

	err := provider.Validate(ValidateArgs{})
	require.ErrorContains(t, err, "service-account OAuth clients require --web")
}
