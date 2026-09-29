package healthcheckextension

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_DefaultValues(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	hcCfg, ok := cfg.(*Config)
	require.True(t, ok, "CreateDefaultConfig must return *Config")
	assert.Equal(t, "localhost:13133", hcCfg.Endpoint)
	assert.Equal(t, "/livez", hcCfg.LivePath)
	assert.Equal(t, "/readyz", hcCfg.ReadyPath)
}

func TestConfig_Validate(t *testing.T) {
	validConfig := func() *Config {
		return &Config{
			Endpoint:  "localhost:13133",
			LivePath:  "/livez",
			ReadyPath: "/readyz",
		}
	}

	tests := []struct {
		name      string
		mutate    func(c *Config)
		wantError string
	}{
		{
			name:   "When all fields are valid it should pass",
			mutate: func(_ *Config) {},
		},
		{
			name:      "When endpoint is empty it should be rejected",
			mutate:    func(c *Config) { c.Endpoint = "" },
			wantError: `"endpoint" must not be empty`,
		},
		{
			name:      "When endpoint has no port it should be rejected",
			mutate:    func(c *Config) { c.Endpoint = "localhost" },
			wantError: "not a valid host:port",
		},
		{
			name:      "When endpoint has empty host it should be rejected",
			mutate:    func(c *Config) { c.Endpoint = ":13133" },
			wantError: "empty host",
		},
		{
			name:      "When endpoint has empty port it should be rejected",
			mutate:    func(c *Config) { c.Endpoint = "localhost:" },
			wantError: "empty port",
		},
		{
			name:      "When livePath is empty it should be rejected",
			mutate:    func(c *Config) { c.LivePath = "" },
			wantError: `"livePath" must not be empty`,
		},
		{
			name:      "When livePath has no leading slash it should be rejected",
			mutate:    func(c *Config) { c.LivePath = "livez" },
			wantError: `must start with /`,
		},
		{
			name:      "When readyPath is empty it should be rejected",
			mutate:    func(c *Config) { c.ReadyPath = "" },
			wantError: `"readyPath" must not be empty`,
		},
		{
			name:      "When readyPath has no leading slash it should be rejected",
			mutate:    func(c *Config) { c.ReadyPath = "readyz" },
			wantError: `must start with /`,
		},
		{
			name:      "When livePath and readyPath are the same it should be rejected",
			mutate:    func(c *Config) { c.LivePath = "/healthz"; c.ReadyPath = "/healthz" },
			wantError: "must not be the same",
		},
		{
			name:      "When livePath contains a query string it should be rejected",
			mutate:    func(c *Config) { c.LivePath = "/livez?check=true" },
			wantError: "query string or fragment",
		},
		{
			name:      "When readyPath contains a fragment it should be rejected",
			mutate:    func(c *Config) { c.ReadyPath = "/readyz#section" },
			wantError: "query string or fragment",
		},
		{
			name:      "When livePath has leading whitespace it should be rejected",
			mutate:    func(c *Config) { c.LivePath = " /livez" },
			wantError: "must start with /",
		},
		{
			name:      "When readyPath has trailing whitespace it should be rejected",
			mutate:    func(c *Config) { c.ReadyPath = "/readyz " },
			wantError: "leading or trailing whitespace",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.ErrorContains(t, err, tc.wantError)
			}
		})
	}
}
