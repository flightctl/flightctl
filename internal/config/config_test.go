package config

import (
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func TestConfig_String_ObfuscatesSensitiveData(t *testing.T) {
	cfg := &Config{
		Database: &dbConfig{
			Type:              "pgsql",
			Hostname:          "localhost",
			Port:              5432,
			Name:              "testdb",
			User:              "testuser",
			Password:          "secretpassword",
			MigrationUser:     "migrator",
			MigrationPassword: "migrationsecret",
		},
		KV: &kvConfig{
			Hostname: "redis-host",
			Port:     6379,
			Password: "redispassword",
		},
	}

	result := cfg.String()

	// Verify sensitive data is redacted
	if strings.Contains(result, "secretpassword") {
		t.Error("Database password should be redacted")
	}
	if strings.Contains(result, "migrationsecret") {
		t.Error("Migration password should be redacted")
	}
	if strings.Contains(result, "redispassword") {
		t.Error("KV password should be redacted")
	}

	// Verify redaction markers are present
	if !strings.Contains(result, "[REDACTED]") {
		t.Error("String() should contain [REDACTED] markers")
	}

	// Verify non-sensitive data is preserved
	if !strings.Contains(result, "localhost") {
		t.Error("Non-sensitive hostname should be preserved")
	}
	if !strings.Contains(result, "testuser") {
		t.Error("Non-sensitive username should be preserved")
	}
}

func TestConfig_String_RedactsAuthClientSecrets(t *testing.T) {
	oidcSecret := "oidc-secret-123"
	oauth2Secret := "oauth2-secret-456" //nolint:gosec // G101: These are test values, not real credentials
	openshiftSecret := "openshift-secret-789"
	aapSecret := "aap-secret-abc"
	pamSecret := "pam-secret-xyz"

	cfg := &Config{
		Auth: &authConfig{
			OIDC: &domain.OIDCProviderSpec{
				ProviderType: domain.Oidc,
				Issuer:       "https://example.com",
				ClientId:     "test-client-id",
				ClientSecret: &oidcSecret,
			},
			OAuth2: &domain.OAuth2ProviderSpec{
				ProviderType:     domain.Oauth2,
				AuthorizationUrl: "https://example.com/auth",
				TokenUrl:         "https://example.com/token",
				UserinfoUrl:      "https://example.com/userinfo",
				ClientId:         "test-client-id",
				ClientSecret:     &oauth2Secret,
			},
			OpenShift: &domain.OpenShiftProviderSpec{
				ProviderType:           domain.Openshift,
				ClusterControlPlaneUrl: lo.ToPtr("https://domain.example.com"),
				AuthorizationUrl:       lo.ToPtr("https://example.com/auth"),
				ClientId:               lo.ToPtr("test-client-id"),
				ClientSecret:           &openshiftSecret,
			},
			AAP: &domain.AapProviderSpec{
				ProviderType:     domain.Aap,
				ApiUrl:           "https://aap.example.com",
				AuthorizationUrl: "https://aap.example.com/auth",
				ClientId:         "test-client-id",
				ClientSecret:     &aapSecret,
			},
			PAMOIDCIssuer: &PAMOIDCIssuer{
				Issuer:       "https://pam.example.com",
				ClientID:     "pam-client-id",
				ClientSecret: pamSecret,
				PAMService:   "flightctl",
			},
		},
	}

	result := cfg.String()

	// Verify all client secrets are redacted
	if strings.Contains(result, oidcSecret) {
		t.Error("OIDC client secret should be redacted")
	}
	if strings.Contains(result, oauth2Secret) {
		t.Error("OAuth2 client secret should be redacted")
	}
	if strings.Contains(result, openshiftSecret) {
		t.Error("OpenShift client secret should be redacted")
	}
	if strings.Contains(result, aapSecret) {
		t.Error("AAP client secret should be redacted")
	}
	if strings.Contains(result, pamSecret) {
		t.Error("PAM OIDC issuer client secret should be redacted")
	}

	// Verify redaction markers are present
	if !strings.Contains(result, "[REDACTED]") {
		t.Error("String() should contain [REDACTED] markers")
	}

	// Verify non-sensitive data is preserved
	if !strings.Contains(result, "test-client-id") {
		t.Error("Non-sensitive client ID should be preserved")
	}
	if !strings.Contains(result, "https://example.com") {
		t.Error("Non-sensitive issuer URL should be preserved")
	}
}

func TestConfig_String_DoesNotMutateOriginal(t *testing.T) {
	oidcSecret := "original-secret"
	cfg := &Config{
		Auth: &authConfig{
			OIDC: &domain.OIDCProviderSpec{
				ProviderType: domain.Oidc,
				Issuer:       "https://example.com",
				ClientId:     "test-client-id",
				ClientSecret: &oidcSecret,
			},
		},
	}

	// Call String() multiple times
	_ = cfg.String()
	_ = cfg.String()

	// Verify original config is not mutated
	if cfg.Auth.OIDC.ClientSecret == nil {
		t.Fatal("Original client secret pointer should not be nil")
	}
	if *cfg.Auth.OIDC.ClientSecret != oidcSecret {
		t.Errorf("Original client secret should not be mutated, got: %s, want: %s", *cfg.Auth.OIDC.ClientSecret, oidcSecret)
	}
}

func TestConfig_String_HandlesNilAuthConfig(t *testing.T) {
	cfg := &Config{
		Database: &dbConfig{
			Type:     "pgsql",
			Hostname: "localhost",
		},
		Auth: nil,
	}

	result := cfg.String()

	// Should not panic and should still produce valid JSON
	if !strings.Contains(result, "localhost") {
		t.Error("Should handle nil auth config gracefully")
	}
}

func TestConfig_String_HandlesNilClientSecrets(t *testing.T) {
	cfg := &Config{
		Auth: &authConfig{
			OIDC: &domain.OIDCProviderSpec{
				ProviderType: domain.Oidc,
				Issuer:       "https://example.com",
				ClientId:     "test-client-id",
				ClientSecret: nil, // No secret configured
			},
			OAuth2: &domain.OAuth2ProviderSpec{
				ProviderType:     domain.Oauth2,
				AuthorizationUrl: "https://example.com/auth",
				TokenUrl:         "https://example.com/token",
				UserinfoUrl:      "https://example.com/userinfo",
				ClientId:         "test-client-id",
				ClientSecret:     nil, // No secret configured
			},
		},
	}

	result := cfg.String()

	// Should not panic with nil secrets
	if !strings.Contains(result, "test-client-id") {
		t.Error("Should handle nil client secrets gracefully")
	}
}
