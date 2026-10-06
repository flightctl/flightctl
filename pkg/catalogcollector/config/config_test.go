package config

import (
	"encoding/json"
	"testing"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/stretchr/testify/require"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		envVars map[string]string
		check   func(t *testing.T, cfg *Config)
		wantErr string
	}{
		// --- Happy paths ---
		{
			name: "When config has one source and one destination and one pipeline it should parse successfully",
			yaml: `
sources:
  testSource/src:
    endpoint: https://example.com
destinations:
  testDestination/dst:
    server: https://dest.example.com
pipelines:
  my-pipeline:
    source: testSource/src
    destination: testDestination/dst
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Sources, 1)
				require.Len(t, cfg.Destinations, 1)
				require.Len(t, cfg.Pipelines, 1)

				src := cfg.Sources["testSource/src"]
				require.Equal(t, catalogcollector.ComponentID{Type: "testSource", Name: "src"}, src.ID)
				requireJSONField(t, src.Config, "endpoint", "https://example.com")

				dst := cfg.Destinations["testDestination/dst"]
				require.Equal(t, catalogcollector.ComponentID{Type: "testDestination", Name: "dst"}, dst.ID)
				requireJSONField(t, dst.Config, "server", "https://dest.example.com")
			},
		},
		{
			name: "When two sources share one destination it should parse successfully",
			yaml: `
sources:
  testSource/alpha:
    endpoint: https://alpha.example.com
  testSource/beta:
    endpoint: https://beta.example.com
destinations:
  testDestination/shared:
    server: https://dest.example.com
pipelines:
  alpha-pipeline:
    source: testSource/alpha
    destination: testDestination/shared
  beta-pipeline:
    source: testSource/beta
    destination: testDestination/shared
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Sources, 2)
				require.Len(t, cfg.Destinations, 1)
				require.Len(t, cfg.Pipelines, 2)
				require.Equal(t, "testDestination/shared", cfg.Pipelines["alpha-pipeline"].Destination)
				require.Equal(t, "testDestination/shared", cfg.Pipelines["beta-pipeline"].Destination)
			},
		},
		{
			name: "When unnamed source and destination are used it should parse successfully",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				src := cfg.Sources["testSource"]
				require.Equal(t, catalogcollector.ComponentID{Type: "testSource"}, src.ID)
				require.Nil(t, src.Config)
				dst := cfg.Destinations["testDestination"]
				require.Equal(t, catalogcollector.ComponentID{Type: "testDestination"}, dst.ID)
				require.Nil(t, dst.Config)
			},
		},
		{
			name: "When component body has fields it should store them as raw config",
			yaml: `
sources:
  testSource/src:
    endpoint: https://example.com
    extra: value
destinations:
  testDestination:
pipelines:
  p:
    source: testSource/src
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				var fields map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(cfg.Sources["testSource/src"].Config, &fields))
				require.Contains(t, fields, "endpoint")
				require.Contains(t, fields, "extra")
			},
		},
		{
			name: "When two instances of the same type are used it should parse both",
			yaml: `
sources:
  http/internal:
    listenAddress: 127.0.0.1:8080
  http/external:
    listenAddress: 0.0.0.0:8081
destinations:
  testDestination:
pipelines:
  internal-pipeline:
    source: http/internal
    destination: testDestination
  external-pipeline:
    source: http/external
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Sources, 2)
				internal := cfg.Sources["http/internal"]
				require.Equal(t, catalogcollector.ComponentType("http"), internal.ID.Type)
				require.Equal(t, "internal", internal.ID.Name)
				external := cfg.Sources["http/external"]
				require.Equal(t, catalogcollector.ComponentType("http"), external.ID.Type)
				require.Equal(t, "external", external.ID.Name)
			},
		},

		// --- Unused components ---
		{
			name: "When a source is defined but unused it should be allowed",
			yaml: `
sources:
  testSource/used:
  testSource/unused:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource/used
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Sources, 2)
				require.Contains(t, cfg.Sources, "testSource/unused")
			},
		},
		{
			name: "When a destination is defined but unused it should be allowed",
			yaml: `
sources:
  testSource:
destinations:
  testDestination/used:
  testDestination/unused:
pipelines:
  p:
    source: testSource
    destination: testDestination/used
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Destinations, 2)
				require.Contains(t, cfg.Destinations, "testDestination/unused")
			},
		},

		// --- Strict decoding ---
		{
			name: "When top-level has unknown field it should return an error",
			yaml: `
sourcess:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			wantErr: "parsing config:",
		},
		{
			name: "When pipeline has unknown field it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
    unknownField: something
`,
			wantErr: "parsing config:",
		},

		// --- Component ID validation ---
		{
			name: "When source key has empty name after separator it should return an error",
			yaml: `
sources:
  http/:
    listenAddress: 127.0.0.1:8080
destinations:
  testDestination:
pipelines:
  p:
    source: http/
    destination: testDestination
`,
			wantErr: "has empty name after separator",
		},
		{
			name: "When source key has multiple separators it should return an error",
			yaml: `
sources:
  a/b/c:
    listenAddress: 127.0.0.1:8080
destinations:
  testDestination:
pipelines:
  p:
    source: a/b/c
    destination: testDestination
`,
			wantErr: "contains multiple separators",
		},

		// --- Reference validation ---
		{
			name: "When pipeline references nonexistent source it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: nonexistent
    destination: testDestination
`,
			wantErr: `source "nonexistent" is not defined`,
		},
		{
			name: "When pipeline references nonexistent destination it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: nonexistent
`,
			wantErr: `destination "nonexistent" is not defined`,
		},
		{
			name: "When two pipelines share one source it should parse successfully",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  pipeline-a:
    source: testSource
    destination: testDestination
  pipeline-b:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Sources, 1)
				require.Len(t, cfg.Pipelines, 2)
				require.Equal(t, "testSource", cfg.Pipelines["pipeline-a"].Source)
				require.Equal(t, "testSource", cfg.Pipelines["pipeline-b"].Source)
			},
		},
		{
			name: "When no pipelines are defined it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
`,
			wantErr: "at least one pipeline must be defined",
		},
		{
			name: "When pipeline source field is empty it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    destination: testDestination
`,
			wantErr: `missing required field "source"`,
		},
		{
			name: "When pipeline destination field is empty it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
`,
			wantErr: `missing required field "destination"`,
		},

		// --- Environment variable references preserved by Parse ---
		{
			name: "When ${env:NAME} is used Parse should preserve the reference as-is",
			yaml: `
sources:
  testSource/src:
    endpoint: "${env:TEST_CC_ENDPOINT}"
destinations:
  testDestination/dst:
    server: "${env:TEST_CC_SERVER}"
pipelines:
  p:
    source: testSource/src
    destination: testDestination/dst
`,
			envVars: map[string]string{
				"TEST_CC_ENDPOINT": "https://resolved.example.com",
				"TEST_CC_SERVER":   "https://dest.resolved.com",
			},
			check: func(t *testing.T, cfg *Config) {
				requireJSONField(t, cfg.Sources["testSource/src"].Config, "endpoint", "${env:TEST_CC_ENDPOINT}")
				requireJSONField(t, cfg.Destinations["testDestination/dst"].Config, "server", "${env:TEST_CC_SERVER}")
			},
		},
		{
			name: "When ${NAME} is used Parse should preserve the reference as-is",
			yaml: `
sources:
  testSource/src:
    endpoint: "${TEST_CC_COMPAT}"
destinations:
  testDestination:
pipelines:
  p:
    source: testSource/src
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				requireJSONField(t, cfg.Sources["testSource/src"].Config, "endpoint", "${TEST_CC_COMPAT}")
			},
		},
		{
			name: "When referenced env var is not set Parse should still succeed",
			yaml: `
sources:
  testSource/src:
    endpoint: "${env:UNSET_VAR_CC_12345}"
destinations:
  testDestination:
pipelines:
  p:
    source: testSource/src
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				requireJSONField(t, cfg.Sources["testSource/src"].Config, "endpoint", "${env:UNSET_VAR_CC_12345}")
			},
		},
		// --- Processor support ---
		{
			name: "When pipeline has processors it should parse the ordered list",
			yaml: `
sources:
  testSource:
processors:
  testFilter/filter:
  testNormalize/normalize:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    processors:
      - testFilter/filter
      - testNormalize/normalize
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Processors, 2)
				require.Equal(t, catalogcollector.ComponentType("testFilter"), cfg.Processors["testFilter/filter"].ID.Type)
				require.Equal(t, catalogcollector.ComponentType("testNormalize"), cfg.Processors["testNormalize/normalize"].ID.Type)
				require.Equal(t, []string{"testFilter/filter", "testNormalize/normalize"}, cfg.Pipelines["p"].Processors)
			},
		},
		{
			name: "When pipeline has no processors field it should default to empty",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Empty(t, cfg.Pipelines["p"].Processors)
			},
		},
		{
			name: "When pipeline references nonexistent processor it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    processors:
      - nonexistent
    destination: testDestination
`,
			wantErr: `processor "nonexistent" is not defined`,
		},
		{
			name: "When a processor is defined but unused it should be allowed",
			yaml: `
sources:
  testSource:
processors:
  testFilter/unused:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Processors, 1)
				require.Contains(t, cfg.Processors, "testFilter/unused")
			},
		},
		{
			name: "When string contains env-like syntax but is not a whole-string reference it should be kept literal",
			yaml: `
sources:
  testSource/src:
    endpoint: "prefix-${env:FOO}"
destinations:
  testDestination:
pipelines:
  p:
    source: testSource/src
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				requireJSONField(t, cfg.Sources["testSource/src"].Config, "endpoint", "prefix-${env:FOO}")
			},
		},
		// --- Extension support ---
		{
			name: "When no extensions section is present it should parse with empty extensions map",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Empty(t, cfg.Extensions)
			},
		},
		{
			name: "When extensions section is present with entries it should parse them",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
extensions:
  authprovider/keycloak:
    issuer: https://keycloak.example.com/realms/test
  cache:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			check: func(t *testing.T, cfg *Config) {
				require.Len(t, cfg.Extensions, 2)

				kc := cfg.Extensions["authprovider/keycloak"]
				require.Equal(t, catalogcollector.ComponentID{Type: "authprovider", Name: "keycloak"}, kc.ID)
				requireJSONField(t, kc.Config, "issuer", "https://keycloak.example.com/realms/test")

				cache := cfg.Extensions["cache"]
				require.Equal(t, catalogcollector.ComponentID{Type: "cache"}, cache.ID)
				require.Nil(t, cache.Config)
			},
		},
		{
			name: "When extension key has empty name after separator it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
extensions:
  authprovider/:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			wantErr: "has empty name after separator",
		},
		{
			name: "When extension key has multiple separators it should return an error",
			yaml: `
sources:
  testSource:
destinations:
  testDestination:
extensions:
  a/b/c:
pipelines:
  p:
    source: testSource
    destination: testDestination
`,
			wantErr: "contains multiple separators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}
			cfg, err := Parse([]byte(tt.yaml))
			if tt.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// requireJSONField asserts that the raw JSON contains a string field with
// the expected value. This helper keeps test assertions readable without
// requiring typed config structs.
func requireJSONField(t *testing.T, raw json.RawMessage, field string, expected string) {
	t.Helper()
	require.NotNil(t, raw, "raw config is nil")
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &fields))
	val, ok := fields[field]
	require.True(t, ok, "field %q not found in raw config", field)
	var s string
	require.NoError(t, json.Unmarshal(val, &s))
	require.Equal(t, expected, s)
}

// minimalYAML returns a valid minimal config YAML with an optional prefix
// prepended. The prefix is injected before the sources block so callers can
// add top-level blocks (e.g. "service:\n  logLevel: debug\n").
func minimalYAML(prefix string) string {
	return prefix + `
sources:
  testsrc:
destinations:
  testdst:
pipelines:
  p:
    source: testsrc
    destination: testdst
`
}

func TestParse_ServiceLogLevel(t *testing.T) {
	tests := []struct {
		name      string
		yaml      string
		wantLevel *string
		wantErr   string
	}{
		{
			name:      "When service block is absent logLevel is nil",
			yaml:      minimalYAML(""),
			wantLevel: nil,
		},
		{
			name:      "When service block is empty logLevel is nil",
			yaml:      minimalYAML("service:\n"),
			wantLevel: nil,
		},
		{
			name:      "When logLevel is debug it is accepted",
			yaml:      minimalYAML("service:\n  logLevel: debug\n"),
			wantLevel: strPtr("debug"),
		},
		{
			name:      "When logLevel is info it is accepted",
			yaml:      minimalYAML("service:\n  logLevel: info\n"),
			wantLevel: strPtr("info"),
		},
		{
			name:      "When logLevel is warn it is accepted",
			yaml:      minimalYAML("service:\n  logLevel: warn\n"),
			wantLevel: strPtr("warn"),
		},
		{
			name:      "When logLevel is error it is accepted",
			yaml:      minimalYAML("service:\n  logLevel: error\n"),
			wantLevel: strPtr("error"),
		},
		{
			name:    "When logLevel is an explicit empty string it is rejected",
			yaml:    minimalYAML("service:\n  logLevel: \"\"\n"),
			wantErr: "service.logLevel",
		},
		{
			name:    "When logLevel is trace it is rejected",
			yaml:    minimalYAML("service:\n  logLevel: trace\n"),
			wantErr: "service.logLevel",
		},
		{
			name:    "When logLevel is fatal it is rejected",
			yaml:    minimalYAML("service:\n  logLevel: fatal\n"),
			wantErr: "service.logLevel",
		},
		{
			name:    "When logLevel is an arbitrary string it is rejected",
			yaml:    minimalYAML("service:\n  logLevel: verbose\n"),
			wantErr: "service.logLevel",
		},
		{
			name:    "When service block contains an unknown field it is rejected",
			yaml:    minimalYAML("service:\n  unknownField: value\n"),
			wantErr: "parsing config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantLevel, cfg.Service.LogLevel)
		})
	}
}

func strPtr(s string) *string { return &s }

// =============================================================================
// Metrics configuration tests
// =============================================================================

func TestParse_ServiceMetrics(t *testing.T) {
	tests := []struct {
		name         string
		yaml         string
		wantNil      bool
		wantEndpoint string
		wantErr      string
	}{
		{
			name:    "When metrics block is absent the pointer is nil (disabled)",
			yaml:    minimalYAML(""),
			wantNil: true,
		},
		{
			name:    "When service block is empty the pointer is nil (disabled)",
			yaml:    minimalYAML("service:\n"),
			wantNil: true,
		},
		{
			name:         "When metrics block is present but empty it should default to localhost:8888",
			yaml:         minimalYAML("service:\n  metrics: {}\n"),
			wantEndpoint: "localhost:8888",
		},
		{
			name:         "When metrics block is present with explicit endpoint it should use it",
			yaml:         minimalYAML("service:\n  metrics:\n    endpoint: 127.0.0.1:9090\n"),
			wantEndpoint: "127.0.0.1:9090",
		},
		{
			name:    "When metrics endpoint has empty host it should be rejected",
			yaml:    minimalYAML("service:\n  metrics:\n    endpoint: :9090\n"),
			wantErr: "service.metrics.endpoint",
		},
		{
			name:    "When metrics endpoint has empty port it should be rejected",
			yaml:    minimalYAML("service:\n  metrics:\n    endpoint: \"localhost:\"\n"),
			wantErr: "service.metrics.endpoint",
		},
		{
			name:    "When metrics endpoint is not a valid host:port it should be rejected",
			yaml:    minimalYAML("service:\n  metrics:\n    endpoint: not-a-hostport\n"),
			wantErr: "service.metrics.endpoint",
		},
		{
			name:    "When legacy address field is used it should be rejected by strict decoding",
			yaml:    minimalYAML("service:\n  metrics:\n    address: 127.0.0.1:9090\n"),
			wantErr: "parsing config",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tc.yaml))
			if tc.wantErr != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			if tc.wantNil {
				require.Nil(t, cfg.Service.Metrics, "expected nil Metrics pointer (disabled)")
			} else {
				require.NotNil(t, cfg.Service.Metrics, "expected non-nil Metrics pointer (enabled)")
				require.Equal(t, tc.wantEndpoint, cfg.Service.Metrics.Endpoint)
			}
		})
	}
}
