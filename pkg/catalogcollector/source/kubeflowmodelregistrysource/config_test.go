package kubeflowmodelregistrysource

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
)

func durationPtr(d time.Duration) *util.Duration {
	ud := util.Duration(d)
	return &ud
}

func validConfig() Config {
	return Config{
		Endpoint: "https://model-registry.example.com",
		Catalog:  "my-catalog",
		Backoff:  pollsource.DefaultBackoffConfig(),
	}
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "valid", mutate: nil},
		{
			name:    "valid with auth",
			mutate:  func(c *Config) { c.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"} },
			wantErr: false,
		},
		{
			name:    "missing endpoint",
			mutate:  func(c *Config) { c.Endpoint = "" },
			wantErr: true,
		},
		{
			name:    "relative endpoint",
			mutate:  func(c *Config) { c.Endpoint = "/api/v1" },
			wantErr: true,
		},
		{
			name:    "missing catalog",
			mutate:  func(c *Config) { c.Catalog = "" },
			wantErr: true,
		},
		{
			name:    "nil auth is allowed (no auth)",
			mutate:  func(c *Config) { c.Auth = nil },
			wantErr: false,
		},
		{
			name:    "negative page size",
			mutate:  func(c *Config) { c.PageSize = -1 },
			wantErr: true,
		},
		{
			name:    "zero page size is allowed (uses default)",
			mutate:  func(c *Config) { c.PageSize = 0 },
			wantErr: false,
		},
		{
			name:    "invalid backoff",
			mutate:  func(c *Config) { c.Backoff.InitialInterval = 0 },
			wantErr: true,
		},
		{
			name: "auth with HTTPS endpoint is valid",
			mutate: func(c *Config) {
				c.Endpoint = "https://model-registry.example.com"
				c.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}
			},
			wantErr: false,
		},
		{
			name: "auth with HTTP endpoint is rejected",
			mutate: func(c *Config) {
				c.Endpoint = "http://model-registry.example.com"
				c.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}
			},
			wantErr: true,
		},
		{
			name: "no auth with HTTP endpoint is valid",
			mutate: func(c *Config) {
				c.Endpoint = "http://model-registry.example.com"
				c.Auth = nil
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			err := cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestConfigDefaults(t *testing.T) {
	cfg := validConfig()
	if cfg.pollInterval() != defaultPollInterval {
		t.Errorf("default poll interval = %v, want %v", cfg.pollInterval(), defaultPollInterval)
	}
	if cfg.requestTimeout() != defaultRequestTimeout {
		t.Errorf("default request timeout = %v, want %v", cfg.requestTimeout(), defaultRequestTimeout)
	}
	if cfg.collectionTimeout() != defaultCollectionTimeout {
		t.Errorf("default collection timeout = %v, want %v", cfg.collectionTimeout(), defaultCollectionTimeout)
	}
	if cfg.pageSize() != defaultPageSize {
		t.Errorf("default page size = %v, want %v", cfg.pageSize(), defaultPageSize)
	}
}

func TestConfigOverrides(t *testing.T) {
	cfg := validConfig()
	cfg.PollInterval = durationPtr(10 * time.Minute)
	cfg.PageSize = 50
	cfg.RequestTimeout = durationPtr(15 * time.Second)
	cfg.CollectionTimeout = durationPtr(2 * time.Minute)

	if cfg.pollInterval() != 10*time.Minute {
		t.Errorf("poll interval = %v, want 10m", cfg.pollInterval())
	}
	if cfg.pageSize() != 50 {
		t.Errorf("page size = %v, want 50", cfg.pageSize())
	}
	if cfg.requestTimeout() != 15*time.Second {
		t.Errorf("request timeout = %v, want 15s", cfg.requestTimeout())
	}
	if cfg.collectionTimeout() != 2*time.Minute {
		t.Errorf("collection timeout = %v, want 2m", cfg.collectionTimeout())
	}
}

func stringPtr(s string) *string { return &s }

func TestModelFilter(t *testing.T) {
	cases := []struct {
		name     string
		config   Config
		expected string
	}{
		{
			name:     "When selection is nil it should return default model filter",
			config:   validConfig(),
			expected: defaultModelFilter,
		},
		{
			name: "When modelFilter is nil it should return default model filter",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{}
				return c
			}(),
			expected: defaultModelFilter,
		},
		{
			name: "When modelFilter is explicitly empty it should return empty string",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{ModelFilter: stringPtr("")}
				return c
			}(),
			expected: "",
		},
		{
			name: "When modelFilter is set it should return that value",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{ModelFilter: stringPtr("custom_filter")}
				return c
			}(),
			expected: "custom_filter",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.config.modelFilter()
			if got != tc.expected {
				t.Errorf("modelFilter() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestVersionFilter(t *testing.T) {
	cases := []struct {
		name     string
		config   Config
		expected string
	}{
		{
			name:     "When selection is nil it should return default version filter",
			config:   validConfig(),
			expected: defaultVersionFilter,
		},
		{
			name: "When versionFilter is nil it should return default version filter",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{}
				return c
			}(),
			expected: defaultVersionFilter,
		},
		{
			name: "When versionFilter is explicitly empty it should return empty string",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{VersionFilter: stringPtr("")}
				return c
			}(),
			expected: "",
		},
		{
			name: "When versionFilter is set it should return that value",
			config: func() Config {
				c := validConfig()
				c.Selection = &SelectionConfig{VersionFilter: stringPtr("state='ARCHIVED'")}
				return c
			}(),
			expected: "state='ARCHIVED'",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.config.versionFilter()
			if got != tc.expected {
				t.Errorf("versionFilter() = %q, want %q", got, tc.expected)
			}
		})
	}
}

func TestSelectionConfig_StrictDecode(t *testing.T) {
	cases := []struct {
		name          string
		json          string
		wantModel     string
		wantVersion   string
		wantDecodeErr bool
	}{
		{
			name:        "When selection is omitted it should use defaults",
			json:        `{"endpoint":"https://mr.example.com","catalog":"cat"}`,
			wantModel:   defaultModelFilter,
			wantVersion: defaultVersionFilter,
		},
		{
			name:        "When custom filters are set it should override defaults",
			json:        `{"endpoint":"https://mr.example.com","catalog":"cat","selection":{"modelFilter":"custom_model","versionFilter":"custom_version"}}`,
			wantModel:   "custom_model",
			wantVersion: "custom_version",
		},
		{
			name:        "When filters are explicitly empty it should disable filtering",
			json:        `{"endpoint":"https://mr.example.com","catalog":"cat","selection":{"modelFilter":"","versionFilter":""}}`,
			wantModel:   "",
			wantVersion: "",
		},
		{
			name:          "When unknown key is present it should fail decode",
			json:          `{"endpoint":"https://mr.example.com","catalog":"cat","selection":{"modelFilter":"x","unknownKey":"y"}}`,
			wantDecodeErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{
				Backoff: pollsource.DefaultBackoffConfig(),
			}
			err := config.DecodeComponent(
				[]byte(tc.json),
				cfg,
				"sources.kubeflowmodelregistry/test",
			)
			if tc.wantDecodeErr {
				if err == nil {
					t.Fatal("expected decode error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeComponent() error: %v", err)
			}
			if cfg.modelFilter() != tc.wantModel {
				t.Errorf("modelFilter() = %q, want %q", cfg.modelFilter(), tc.wantModel)
			}
			if cfg.versionFilter() != tc.wantVersion {
				t.Errorf("versionFilter() = %q, want %q", cfg.versionFilter(), tc.wantVersion)
			}
		})
	}
}

func TestSelectionConfig_IndependentFields(t *testing.T) {
	// Verify that setting one field does not affect the other.
	cfg := validConfig()
	cfg.Selection = &SelectionConfig{
		ModelFilter: stringPtr("custom_model"),
		// VersionFilter left nil -> uses default.
	}

	if cfg.modelFilter() != "custom_model" {
		t.Errorf("modelFilter() = %q, want %q", cfg.modelFilter(), "custom_model")
	}
	if cfg.versionFilter() != defaultVersionFilter {
		t.Errorf("versionFilter() = %q, want %q", cfg.versionFilter(), defaultVersionFilter)
	}
}
