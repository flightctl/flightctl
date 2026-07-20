package login

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetTokenProxyURL(t *testing.T) {
	tests := []struct {
		name         string
		apiServerURL string
		providerName string
		want         string
		wantErr      bool
	}{
		{
			name:         "When API server URL has no trailing slash it should produce the correct token proxy URL",
			apiServerURL: "https://api.example.com",
			providerName: "keycloak-oidc",
			want:         "https://api.example.com/api/v1/auth/keycloak-oidc/token",
		},
		{
			name:         "When API server URL has a trailing slash it should produce the correct token proxy URL without double slash",
			apiServerURL: "https://api.example.com/",
			providerName: "keycloak-oidc",
			want:         "https://api.example.com/api/v1/auth/keycloak-oidc/token",
		},
		{
			name:         "When API server URL has a port it should be preserved",
			apiServerURL: "https://api.example.com:8443",
			providerName: "my-provider",
			want:         "https://api.example.com:8443/api/v1/auth/my-provider/token",
		},
		{
			name:         "When API server URL has a port and trailing slash it should produce the correct token proxy URL",
			apiServerURL: "https://api.example.com:8443/",
			providerName: "my-provider",
			want:         "https://api.example.com:8443/api/v1/auth/my-provider/token",
		},
		{
			name:         "When API server URL uses http it should be preserved",
			apiServerURL: "http://192.168.1.100:9100",
			providerName: "keycloak",
			want:         "http://192.168.1.100:9100/api/v1/auth/keycloak/token",
		},
		{
			name:         "When API server URL is empty it should return an error",
			apiServerURL: "",
			providerName: "provider",
			wantErr:      true,
		},
		{
			name:         "When API server URL has no scheme it should return an error",
			apiServerURL: "api.example.com",
			providerName: "provider",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getTokenProxyURL(tt.apiServerURL, tt.providerName)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
