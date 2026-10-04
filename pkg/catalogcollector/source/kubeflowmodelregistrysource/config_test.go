package kubeflowmodelregistrysource

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/util"
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
