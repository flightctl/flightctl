package config

import (
	"encoding/json"
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

func TestVulnerabilityConfig_BackendJSONRoundTrip(t *testing.T) {
	var v VulnerabilityConfig
	if err := json.Unmarshal([]byte(`{"backend":"trustify"}`), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.Backend != VulnerabilityBackendTrustify {
		t.Errorf("When backend is set in JSON it should deserialize to the enum, got %q", v.Backend)
	}

	out, err := json.Marshal(VulnerabilityConfig{Backend: VulnerabilityBackendTrustify})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"backend":"trustify"`) {
		t.Errorf("When backend is set it should serialize to JSON, got %s", out)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_Backend(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_BACKEND", "trustify")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil {
		t.Fatal("When the backend env var is set it should initialize VulnerabilityReporting")
	}
	if c.VulnerabilityReporting.Backend != VulnerabilityBackendTrustify {
		t.Errorf("When the backend env var is set it should populate Backend, got %q", c.VulnerabilityReporting.Backend)
	}
}

func TestVulnerabilityConfig_QuayJSONRoundTrip(t *testing.T) {
	var v VulnerabilityConfig
	in := `{"backend":"quay","quay":{"endpoint":"https://quay.io","token":"tok","maxConcurrentRequests":7}}`
	if err := json.Unmarshal([]byte(in), &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v.Backend != VulnerabilityBackendQuay {
		t.Errorf("When backend is quay it should deserialize to the enum, got %q", v.Backend)
	}
	if v.Quay == nil {
		t.Fatal("When a quay block is present it should deserialize into Quay")
	}
	if v.Quay.Endpoint != "https://quay.io" {
		t.Errorf("Quay endpoint = %q, want https://quay.io", v.Quay.Endpoint)
	}
	if string(v.Quay.Token) != "tok" {
		t.Errorf("Quay token = %q, want tok", string(v.Quay.Token))
	}
	if v.Quay.MaxConcurrentRequests != 7 {
		t.Errorf("Quay maxConcurrentRequests = %d, want 7", v.Quay.MaxConcurrentRequests)
	}

	out, err := json.Marshal(VulnerabilityConfig{Backend: VulnerabilityBackendQuay})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"backend":"quay"`) {
		t.Errorf("When backend is quay it should serialize to JSON, got %s", out)
	}
}

func TestVulnerabilityConfig_EffectiveBackend(t *testing.T) {
	tests := []struct {
		name string
		cfg  VulnerabilityConfig
		want VulnerabilityBackend
	}{
		{
			name: "When backend is set explicitly it should be returned",
			cfg:  VulnerabilityConfig{Backend: VulnerabilityBackendQuay, Trustify: &TrustifyConfig{}},
			want: VulnerabilityBackendQuay,
		},
		{
			name: "When backend is empty and a Trustify block with endpoint is present it should default to trustify",
			cfg:  VulnerabilityConfig{Trustify: &TrustifyConfig{Endpoint: "https://trustify.example.com"}},
			want: VulnerabilityBackendTrustify,
		},
		{
			name: "When backend is empty and no Trustify block is present it should be empty",
			cfg:  VulnerabilityConfig{},
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.EffectiveBackend(); got != tt.want {
				t.Errorf("EffectiveBackend() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_Quay(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_BACKEND", "quay")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_TOKEN", "secret-token")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_MAX_CONCURRENT_REQUESTS", "9")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
		t.Fatal("When quay env vars are set it should initialize VulnerabilityReporting.Quay")
	}
	if c.VulnerabilityReporting.Backend != VulnerabilityBackendQuay {
		t.Errorf("Backend = %q, want quay", c.VulnerabilityReporting.Backend)
	}
	q := c.VulnerabilityReporting.Quay
	if q.Endpoint != "https://quay.example.com" {
		t.Errorf("Quay endpoint = %q", q.Endpoint)
	}
	if string(q.Token) != "secret-token" {
		t.Errorf("Quay token = %q", string(q.Token))
	}
	if q.MaxConcurrentRequests != 9 {
		t.Errorf("Quay maxConcurrentRequests = %d, want 9", q.MaxConcurrentRequests)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_QuayInvalidMaxConcurrent(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_MAX_CONCURRENT_REQUESTS", "not-a-number")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
		t.Fatal("When a quay env var is set it should initialize VulnerabilityReporting.Quay")
	}
	if c.VulnerabilityReporting.Quay.MaxConcurrentRequests != 0 {
		t.Errorf("When maxConcurrentRequests is invalid it should be ignored, got %d", c.VulnerabilityReporting.Quay.MaxConcurrentRequests)
	}
}

func TestApplyVulnerabilityReportingDefaults_QuayMaxConcurrent(t *testing.T) {
	c := &Config{VulnerabilityReporting: &VulnerabilityConfig{Quay: &QuayConfig{Endpoint: "https://quay.io"}}}
	applyVulnerabilityReportingDefaults(c)

	if c.VulnerabilityReporting.Quay.MaxConcurrentRequests != DefaultQuayMaxConcurrentRequests {
		t.Errorf("When maxConcurrentRequests is unset it should default to %d, got %d",
			DefaultQuayMaxConcurrentRequests, c.VulnerabilityReporting.Quay.MaxConcurrentRequests)
	}

	// An explicit value must be preserved.
	c2 := &Config{VulnerabilityReporting: &VulnerabilityConfig{Quay: &QuayConfig{MaxConcurrentRequests: 3}}}
	applyVulnerabilityReportingDefaults(c2)
	if c2.VulnerabilityReporting.Quay.MaxConcurrentRequests != 3 {
		t.Errorf("When maxConcurrentRequests is set it should be preserved, got %d", c2.VulnerabilityReporting.Quay.MaxConcurrentRequests)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_InvalidBackend(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_BACKEND", "typo")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting != nil && c.VulnerabilityReporting.Backend != "" {
		t.Errorf("When backend env var is invalid it should be ignored, got %q", c.VulnerabilityReporting.Backend)
	}
}

func TestApplyVulnerabilityReportingEnvVarOverrides_NegativeMaxConcurrent(t *testing.T) {
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_ENDPOINT", "https://quay.example.com")
	t.Setenv("FLIGHTCTL_VULNERABILITY_REPORTING_QUAY_MAX_CONCURRENT_REQUESTS", "-5")

	c := &Config{}
	applyVulnerabilityReportingEnvVarOverrides(c)

	if c.VulnerabilityReporting == nil || c.VulnerabilityReporting.Quay == nil {
		t.Fatal("When a quay env var is set it should initialize VulnerabilityReporting.Quay")
	}
	if c.VulnerabilityReporting.Quay.MaxConcurrentRequests != 0 {
		t.Errorf("When maxConcurrentRequests is negative it should be ignored, got %d", c.VulnerabilityReporting.Quay.MaxConcurrentRequests)
	}
}

func TestVulnerabilityBackend_UnmarshalJSON_Invalid(t *testing.T) {
	var v VulnerabilityConfig
	in := `{"backend":"invalid-backend"}`
	err := json.Unmarshal([]byte(in), &v)
	if err == nil {
		t.Fatal("When backend is invalid it should error during unmarshal")
	}
	if !strings.Contains(err.Error(), "unknown vulnerability backend") {
		t.Errorf("Error should mention unknown backend, got: %v", err)
	}
}

func TestVulnerabilityConfig_Validate_NegativeMaxConcurrent(t *testing.T) {
	v := &VulnerabilityConfig{
		Quay: &QuayConfig{
			Endpoint:              "https://quay.io",
			MaxConcurrentRequests: -5,
		},
	}
	err := v.Validate()
	if err == nil {
		t.Fatal("When maxConcurrentRequests is negative Validate should error")
	}
	if !strings.Contains(err.Error(), "non-negative") {
		t.Errorf("Error should mention non-negative requirement, got: %v", err)
	}
}

func TestVulnerabilityConfig_Validate_ValidConfig(t *testing.T) {
	v := &VulnerabilityConfig{
		Backend: VulnerabilityBackendQuay,
		Quay: &QuayConfig{
			Endpoint:              "https://quay.io",
			MaxConcurrentRequests: 5,
		},
	}
	err := v.Validate()
	if err != nil {
		t.Errorf("Valid config should not error, got: %v", err)
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

func TestValidateDeltaGenerationDefaultRepository(t *testing.T) {
	t.Run("When repository and namespace are both set it should fail", func(t *testing.T) {
		cfg := NewDefault()
		cfg.DeltaGeneration = &DeltaGenerationConfig{
			DefaultRepository: &DefaultRepositoryConfig{
				Registry:   "my-registry.com",
				Repository: lo.ToPtr("my-org/diffs"),
				Namespace:  lo.ToPtr("my-org"),
			},
		}
		require.Error(t, Validate(cfg))
	})

	t.Run("When credentials are set without registry it should fail", func(t *testing.T) {
		cfg := NewDefault()
		cfg.DeltaGeneration = &DeltaGenerationConfig{
			DefaultRepository: &DefaultRepositoryConfig{
				Username: "delta-user",
				Password: "delta-pass",
			},
		}
		require.Error(t, Validate(cfg))
	})

	t.Run("When scheme is invalid it should fail", func(t *testing.T) {
		cfg := NewDefault()
		cfg.DeltaGeneration = &DeltaGenerationConfig{
			DefaultRepository: &DefaultRepositoryConfig{
				Registry: "my-registry.com",
				Scheme:   lo.ToPtr("ftp"),
			},
		}
		require.Error(t, Validate(cfg))
	})

	t.Run("When only repository is set it should pass", func(t *testing.T) {
		cfg := NewDefault()
		cfg.DeltaGeneration = &DeltaGenerationConfig{
			DefaultRepository: &DefaultRepositoryConfig{
				Registry:   "my-registry.com",
				Repository: lo.ToPtr("my-org/diffs"),
			},
		}
		require.NoError(t, Validate(cfg))
	})
}
