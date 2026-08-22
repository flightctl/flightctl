package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
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
				ClientSecret: oidcSecret,
			},
			OAuth2: &domain.OAuth2ProviderSpec{
				ProviderType:     domain.Oauth2,
				AuthorizationUrl: "https://example.com/auth",
				TokenUrl:         "https://example.com/token",
				UserinfoUrl:      "https://example.com/userinfo",
				ClientId:         "test-client-id",
				ClientSecret:     oauth2Secret,
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
				ClientSecret:     aapSecret,
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
				ClientSecret: oidcSecret,
			},
		},
	}

	// Call String() multiple times
	_ = cfg.String()
	_ = cfg.String()

	// Verify original config is not mutated
	if cfg.Auth.OIDC.ClientSecret != oidcSecret {
		t.Errorf("Original client secret should not be mutated, got: %s, want: %s", cfg.Auth.OIDC.ClientSecret, oidcSecret)
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

func TestConfig_String_HandlesEmptyClientSecrets(t *testing.T) {
	cfg := &Config{
		Auth: &authConfig{
			OIDC: &domain.OIDCProviderSpec{
				ProviderType: domain.Oidc,
				Issuer:       "https://example.com",
				ClientId:     "test-client-id",
				ClientSecret: "", // No secret configured
			},
			OAuth2: &domain.OAuth2ProviderSpec{
				ProviderType:     domain.Oauth2,
				AuthorizationUrl: "https://example.com/auth",
				TokenUrl:         "https://example.com/token",
				UserinfoUrl:      "https://example.com/userinfo",
				ClientId:         "test-client-id",
				ClientSecret:     "", // No secret configured
			},
		},
	}

	result := cfg.String()

	// Should not panic with empty secrets
	if !strings.Contains(result, "test-client-id") {
		t.Error("Should handle empty client secrets gracefully")
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
	return path
}

func TestLoadDeltaGenerationDefaultRepository(t *testing.T) {
	t.Run("When YAML has registry and repository it should load them and ignore username and password", func(t *testing.T) {
		path := writeTempConfig(t, `
deltaGeneration:
  defaultRepository:
    registry: my-registry.com
    repository: my-org/diffs
    scheme: https
    skipServerVerification: true
    username: from-yaml
    password: from-yaml-secret
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		require.NotNil(t, cfg.DeltaGeneration)
		require.NotNil(t, cfg.DeltaGeneration.DefaultRepository)
		dr := cfg.DeltaGeneration.DefaultRepository
		require.Equal(t, "my-registry.com", dr.Registry)
		require.Equal(t, "my-org/diffs", lo.FromPtr(dr.Repository))
		require.Equal(t, "https", lo.FromPtr(dr.Scheme))
		require.True(t, lo.FromPtr(dr.SkipServerVerification))
		require.Empty(t, dr.Username)
		require.Empty(t, string(dr.Password))
	})

	t.Run("When YAML has namespace it should load it", func(t *testing.T) {
		path := writeTempConfig(t, `
deltaGeneration:
  defaultRepository:
    registry: my-registry.com
    namespace: my-org
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		require.Equal(t, "my-org", lo.FromPtr(cfg.DeltaGeneration.DefaultRepository.Namespace))
	})

	t.Run("When env vars are set it should populate username and password", func(t *testing.T) {
		t.Setenv("DELTA_GENERATION_DEFAULT_REPOSITORY_USERNAME", "delta-user")
		t.Setenv("DELTA_GENERATION_DEFAULT_REPOSITORY_PASSWORD", "delta-pass")
		path := writeTempConfig(t, `
deltaGeneration:
  defaultRepository:
    registry: my-registry.com
    repository: my-org/diffs
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		dr := cfg.DeltaGeneration.DefaultRepository
		require.Equal(t, "delta-user", dr.Username)
		require.Equal(t, "delta-pass", string(dr.Password))
	})

	t.Run("When env vars are set without YAML deltaGeneration it should still populate credentials", func(t *testing.T) {
		t.Setenv("DELTA_GENERATION_DEFAULT_REPOSITORY_USERNAME", "delta-user")
		t.Setenv("DELTA_GENERATION_DEFAULT_REPOSITORY_PASSWORD", "delta-pass")
		path := writeTempConfig(t, "{}\n")
		cfg, err := Load(path)
		require.NoError(t, err)
		require.NotNil(t, cfg.DeltaGeneration)
		require.NotNil(t, cfg.DeltaGeneration.DefaultRepository)
		require.Equal(t, "delta-user", cfg.DeltaGeneration.DefaultRepository.Username)
		require.Equal(t, "delta-pass", string(cfg.DeltaGeneration.DefaultRepository.Password))
	})
}

func TestEffectiveMaxConcurrentDeltaGenerations(t *testing.T) {
	t.Run("When config is omitted it should default to 2", func(t *testing.T) {
		require.Equal(t, 2, (*DeltaGenerationConfig)(nil).EffectiveMaxConcurrentDeltaGenerations())
		require.Equal(t, 2, (&DeltaGenerationConfig{}).EffectiveMaxConcurrentDeltaGenerations())
	})

	t.Run("When value is 0 or negative it should default to 2", func(t *testing.T) {
		require.Equal(t, 2, (&DeltaGenerationConfig{MaxConcurrentDeltaGenerations: 0}).EffectiveMaxConcurrentDeltaGenerations())
		require.Equal(t, 2, (&DeltaGenerationConfig{MaxConcurrentDeltaGenerations: -1}).EffectiveMaxConcurrentDeltaGenerations())
	})

	t.Run("When YAML sets maxConcurrentDeltaGenerations to 4 it should be 4", func(t *testing.T) {
		path := writeTempConfig(t, `
deltaGeneration:
  maxConcurrentDeltaGenerations: 4
`)
		cfg, err := Load(path)
		require.NoError(t, err)
		require.Equal(t, 4, cfg.DeltaGeneration.EffectiveMaxConcurrentDeltaGenerations())
	})

	t.Run("When NewDefault it should Effective 2", func(t *testing.T) {
		require.Equal(t, 2, NewDefault().DeltaGeneration.EffectiveMaxConcurrentDeltaGenerations())
	})
}

func TestDefaultRepositoryConfigOciRepoSpec(t *testing.T) {
	t.Run("When registry is empty it should return nil", func(t *testing.T) {
		require.Nil(t, (&DefaultRepositoryConfig{}).OciRepoSpec())
		require.Nil(t, (*DefaultRepositoryConfig)(nil).OciRepoSpec())
	})

	t.Run("When username and password are set it should carry Docker auth", func(t *testing.T) {
		spec := (&DefaultRepositoryConfig{
			Registry:   "my-registry.com",
			Repository: lo.ToPtr("my-org/diffs"),
			Username:   "delta-user",
			Password:   "delta-pass",
		}).OciRepoSpec()
		require.NotNil(t, spec)
		require.Equal(t, "my-registry.com", spec.Registry)
		require.Equal(t, "my-org/diffs", lo.FromPtr(spec.Repository))
		require.NotNil(t, spec.OciAuth)
		docker, err := spec.OciAuth.AsDockerAuth()
		require.NoError(t, err)
		require.Equal(t, "delta-user", docker.Username)
		require.Equal(t, "delta-pass", docker.Password)
	})

	t.Run("When scheme is set it should copy it onto the OCI spec", func(t *testing.T) {
		spec := (&DefaultRepositoryConfig{
			Registry: "my-registry.com",
			Scheme:   lo.ToPtr("http"),
		}).OciRepoSpec()
		require.NotNil(t, spec)
		require.Equal(t, domain.OciRepoSpecScheme("http"), lo.FromPtr(spec.Scheme))
		require.Nil(t, spec.OciAuth)
	})

	t.Run("When only username is set it should omit OCI auth", func(t *testing.T) {
		spec := (&DefaultRepositoryConfig{
			Registry: "my-registry.com",
			Username: "delta-user",
		}).OciRepoSpec()
		require.NotNil(t, spec)
		require.Nil(t, spec.OciAuth)
	})
}
