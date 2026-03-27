package provider

import (
	"testing"
)

func TestNormalizeIssuerURL(t *testing.T) {
	tests := []struct {
		name    string
		issuer  string
		want    string
		wantErr bool
	}{
		{"no trailing slash", "https://example.com", "https://example.com", false},
		{"trailing slash", "https://example.com/", "https://example.com", false},
		{"with path", "https://example.com/oidc", "https://example.com/oidc", false},
		{"with path and trailing slash", "https://example.com/oidc/", "https://example.com/oidc", false},
		{"empty", "", "", true},
		{"invalid no scheme", "example.com", "", true},
		{"invalid no host", "https://", "", true},
		{"valid with port", "https://example.com:8444/", "https://example.com:8444", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NormalizeIssuerURL(tt.issuer)
			if (err != nil) != tt.wantErr {
				t.Errorf("NormalizeIssuerURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("NormalizeIssuerURL() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoveryURL(t *testing.T) {
	tests := []struct {
		name    string
		issuer  string
		want    string
		wantErr bool
	}{
		{"no trailing slash", "https://example.com", "https://example.com/.well-known/openid-configuration", false},
		{"trailing slash no double slash", "https://example.com/", "https://example.com/.well-known/openid-configuration", false},
		{"with path", "https://example.com/oidc", "https://example.com/oidc/.well-known/openid-configuration", false},
		{"empty", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DiscoveryURL(tt.issuer)
			if (err != nil) != tt.wantErr {
				t.Errorf("DiscoveryURL() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("DiscoveryURL() = %v, want %v", got, tt.want)
			}
		})
	}
}
