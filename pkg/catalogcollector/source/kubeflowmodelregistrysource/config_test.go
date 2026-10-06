package kubeflowmodelregistrysource

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// TestConfigValidate_ErrorContext covers the remaining rejection paths and
// asserts that each error names the offending field and the reason, so that an
// operator can fix the collector configuration without reading the source.
func TestConfigValidate_ErrorContext(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*Config)
		wantErrs []string
	}{
		// --- endpoint ---
		{
			name:     "when the endpoint has leading whitespace it should fail",
			mutate:   func(c *Config) { c.Endpoint = "  https://registry.example.com" },
			wantErrs: []string{"endpoint", "whitespace"},
		},
		{
			name:     "when the endpoint has trailing whitespace it should fail",
			mutate:   func(c *Config) { c.Endpoint = "https://registry.example.com\n" },
			wantErrs: []string{"endpoint", "whitespace"},
		},
		{
			name:     "when the endpoint is only whitespace it should report a missing field",
			mutate:   func(c *Config) { c.Endpoint = "   " },
			wantErrs: []string{"missing required field", "endpoint"},
		},
		{
			name:     "when the endpoint carries userinfo it should fail",
			mutate:   func(c *Config) { c.Endpoint = "https://user:pass@registry.example.com" },
			wantErrs: []string{"endpoint", "user information", "auth.authenticator"},
		},
		{
			name:     "when the endpoint carries a bare username it should fail",
			mutate:   func(c *Config) { c.Endpoint = "https://user@registry.example.com" },
			wantErrs: []string{"endpoint", "user information"},
		},
		{
			name:     "when the endpoint carries a query it should fail",
			mutate:   func(c *Config) { c.Endpoint = "https://registry.example.com?token=abc" },
			wantErrs: []string{"endpoint", "query parameters"},
		},
		{
			name:     "when the endpoint uses the ftp scheme it should fail",
			mutate:   func(c *Config) { c.Endpoint = "ftp://registry.example.com" },
			wantErrs: []string{"endpoint scheme", "http", "https"},
		},
		{
			name:     "when the endpoint uses the file scheme it should fail",
			mutate:   func(c *Config) { c.Endpoint = "file:///etc/hosts" },
			wantErrs: []string{"endpoint"},
		},
		{
			name:     "when the endpoint uses the grpc scheme it should fail",
			mutate:   func(c *Config) { c.Endpoint = "grpc://registry.example.com" },
			wantErrs: []string{"endpoint scheme"},
		},
		{
			name:     "when the endpoint has no host it should fail",
			mutate:   func(c *Config) { c.Endpoint = "https://" },
			wantErrs: []string{"endpoint must be an absolute HTTP or HTTPS URL"},
		},
		{
			name:     "when the endpoint is a bare host it should fail",
			mutate:   func(c *Config) { c.Endpoint = "registry.example.com" },
			wantErrs: []string{"endpoint must be an absolute HTTP or HTTPS URL"},
		},

		// --- catalog ---
		{
			name:     "when the catalog has surrounding whitespace it should fail",
			mutate:   func(c *Config) { c.Catalog = " my-catalog " },
			wantErrs: []string{"catalog", "whitespace"},
		},
		{
			name:     "when the catalog is only whitespace it should report a missing field",
			mutate:   func(c *Config) { c.Catalog = "\t" },
			wantErrs: []string{"missing required field", "catalog"},
		},
		{
			name:     "when the catalog is uppercase it should fail",
			mutate:   func(c *Config) { c.Catalog = "MyCatalog" },
			wantErrs: []string{"catalog must be a valid Flightctl resource name"},
		},
		{
			name:     "when the catalog starts with a hyphen it should fail",
			mutate:   func(c *Config) { c.Catalog = "-my-catalog" },
			wantErrs: []string{"catalog must be a valid Flightctl resource name"},
		},
		{
			name:     "when the catalog contains an underscore it should fail",
			mutate:   func(c *Config) { c.Catalog = "my_catalog" },
			wantErrs: []string{"catalog must be a valid Flightctl resource name"},
		},
		{
			name:     "when the catalog exceeds the name length limit it should fail",
			mutate:   func(c *Config) { c.Catalog = strings.Repeat("a", 254) },
			wantErrs: []string{"catalog must be a valid Flightctl resource name"},
		},

		// --- timeouts and intervals ---
		{
			name:     "when pollInterval is zero it should fail",
			mutate:   func(c *Config) { c.PollInterval = durationPtr(0) },
			wantErrs: []string{"pollInterval must be positive"},
		},
		{
			name:     "when pollInterval is negative it should fail",
			mutate:   func(c *Config) { c.PollInterval = durationPtr(-time.Second) },
			wantErrs: []string{"pollInterval must be positive"},
		},
		{
			name:     "when requestTimeout is zero it should fail",
			mutate:   func(c *Config) { c.RequestTimeout = durationPtr(0) },
			wantErrs: []string{"requestTimeout must be positive"},
		},
		{
			name:     "when requestTimeout is negative it should fail",
			mutate:   func(c *Config) { c.RequestTimeout = durationPtr(-time.Minute) },
			wantErrs: []string{"requestTimeout must be positive"},
		},
		{
			name:     "when collectionTimeout is zero it should fail",
			mutate:   func(c *Config) { c.CollectionTimeout = durationPtr(0) },
			wantErrs: []string{"collectionTimeout must be positive"},
		},
		{
			name:     "when collectionTimeout is negative it should fail",
			mutate:   func(c *Config) { c.CollectionTimeout = durationPtr(-time.Hour) },
			wantErrs: []string{"collectionTimeout must be positive"},
		},

		// --- page size ---
		{
			name:     "when pageSize is negative it should fail",
			mutate:   func(c *Config) { c.PageSize = -1 },
			wantErrs: []string{"pageSize must be non-negative", "-1"},
		},
		{
			name:     "when pageSize exceeds the 32-bit limit it should fail",
			mutate:   func(c *Config) { c.PageSize = math.MaxInt32 + 1 },
			wantErrs: []string{"pageSize must not exceed", "2147483647"},
		},

		// --- auth ---
		{
			name:     "when the authenticator is empty it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: ""} },
			wantErrs: []string{"auth.authenticator must not be empty"},
		},
		{
			name:     "when the authenticator is only whitespace it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: "   "} },
			wantErrs: []string{"auth.authenticator must not be empty"},
		},
		{
			name:     "when the authenticator has surrounding whitespace it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: " bearertokenauth/mr"} },
			wantErrs: []string{"auth.authenticator", "whitespace"},
		},
		{
			name:     "when the authenticator has an empty type it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: "/mr"} },
			wantErrs: []string{"auth.authenticator must be a valid component ID", "empty type"},
		},
		{
			name:     "when the authenticator has an empty name it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: "bearertokenauth/"} },
			wantErrs: []string{"auth.authenticator must be a valid component ID", "empty name"},
		},
		{
			name:     "when the authenticator has multiple separators it should fail",
			mutate:   func(c *Config) { c.Auth = &AuthConfig{Authenticator: "a/b/c"} },
			wantErrs: []string{"auth.authenticator must be a valid component ID", "multiple separators"},
		},
		{
			name: "when auth is combined with a plain HTTP endpoint it should fail",
			mutate: func(c *Config) {
				c.Endpoint = "http://registry.example.com"
				c.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}
			},
			wantErrs: []string{"auth requires an HTTPS endpoint", "unencrypted"},
		},

		// --- backoff ---
		{
			name:     "when the backoff multiplier is below one it should fail",
			mutate:   func(c *Config) { c.Backoff.Multiplier = 0.5 },
			wantErrs: []string{"backoff", "multiplier"},
		},
		{
			name: "when the backoff initial interval exceeds the maximum it should fail",
			mutate: func(c *Config) {
				c.Backoff.InitialInterval = util.Duration(time.Hour)
				c.Backoff.MaxInterval = util.Duration(time.Second)
			},
			wantErrs: []string{"backoff", "initialInterval"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %v", tc.wantErrs)
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

// TestConfigValidate_EndpointFragment_Rejected verifies that an endpoint
// containing a literal "#" is rejected.
//
// url.ParseRequestURI documents that its argument "is assumed not to have a
// #fragment suffix", so it never populates URL.Fragment and instead folds
// everything after "#" into URL.Path. Inspecting parsed.Fragment therefore
// cannot catch this; the raw string has to be checked before parsing.
//
// Accepting a fragment silently breaks every request: the generated client
// appends the Model Registry API path to the configured server URL, and
// net/http then treats "#section/api/model_registry/..." as a fragment and
// strips it, sending requests to the truncated path instead. A trailing "#"
// with an empty fragment truncates identically.
func TestConfigValidate_EndpointFragment_Rejected(t *testing.T) {
	endpoints := []string{
		"https://registry.example.com/base#section",
		"https://registry.example.com/base#",
		"https://registry.example.com#",
		"https://registry.example.com#section",
		"https://registry.example.com/#/ui/models",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			// url.ParseRequestURI folds the fragment into the path rather
			// than reporting it, which is why validation cannot rely on it.
			parsed, parseErr := url.ParseRequestURI(endpoint)
			if parseErr == nil && parsed.Fragment != "" {
				t.Errorf(
					"url.ParseRequestURI now reports fragment %q; "+
						"the raw-string check may be replaceable",
					parsed.Fragment,
				)
			}

			cfg := validConfig()
			cfg.Endpoint = endpoint

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() with endpoint %q = nil, want a fragment error", endpoint)
			}
			if !strings.Contains(err.Error(), "must not contain a fragment") {
				t.Errorf("error %q does not report the fragment", err.Error())
			}
		})
	}
}

// TestConfigValidate_EndpointFragmentRejectionDoesNotOverReject verifies that
// rejecting the literal "#" leaves legitimate reverse-proxy endpoints and
// percent-encoded "%23" path segments valid.
func TestConfigValidate_EndpointFragmentRejectionDoesNotOverReject(t *testing.T) {
	endpoints := []string{
		"https://registry.example.com/base",
		"https://registry.example.com/proxy/prefix",
		"https://registry.example.com/proxy/prefix/",
		"https://registry.example.com:8443/model-registry",
		"https://registry.example.com/base%23section",
		"https://registry.example.com/%23/models",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoint = endpoint
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with endpoint %q = %v, want nil", endpoint, err)
			}
		})
	}
}

// TestConfigEndpoint_ProxyPrefixPreservesAPIPath verifies end to end, through
// the generated SDK, that a validated reverse-proxy endpoint keeps its prefix
// and still receives the appended Model Registry API path. This is the
// behaviour a fragment would have silently destroyed.
func TestConfigEndpoint_ProxyPrefixPreservesAPIPath(t *testing.T) {
	cases := []struct {
		name     string
		prefix   string
		wantPath string
	}{
		{
			name:     "when the endpoint has a proxy prefix it should keep it",
			prefix:   "/proxy/prefix",
			wantPath: "/proxy/prefix/api/model_registry/v1alpha3/registered_models",
		},
		{
			name:     "when the endpoint has no prefix it should use the API path alone",
			prefix:   "",
			wantPath: "/api/model_registry/v1alpha3/registered_models",
		},
		{
			name:     "when the prefix contains an encoded hash it should be preserved",
			prefix:   "/base%23section",
			wantPath: "/base#section/api/model_registry/v1alpha3/registered_models",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath, gotRawPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotRawPath = r.URL.EscapedPath()
				w.Header().Set("Content-Type", "application/json")
				if _, err := w.Write([]byte(`{"items":[],"nextPageToken":""}`)); err != nil {
					t.Errorf("writing response: %v", err)
				}
			}))
			defer srv.Close()

			cfg := validConfig()
			cfg.Endpoint = srv.URL + tc.prefix
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate() with endpoint %q = %v, want nil", cfg.Endpoint, err)
			}

			client, err := newOpenapiClient(strings.TrimRight(cfg.Endpoint, "/"), "10")
			if err != nil {
				t.Fatalf("newOpenapiClient: %v", err)
			}
			if _, err := client.ListRegisteredModels(context.Background(), ""); err != nil {
				t.Fatalf("ListRegisteredModels: %v", err)
			}

			if gotPath != tc.wantPath {
				t.Errorf("request path = %q, want %q (raw %q)", gotPath, tc.wantPath, gotRawPath)
			}
		})
	}
}

// TestConfigValidate_AcceptedEndpoints covers endpoint shapes that must stay
// valid so that the stricter checks above do not over-reject.
func TestConfigValidate_AcceptedEndpoints(t *testing.T) {
	endpoints := []string{
		"https://registry.example.com",
		"https://registry.example.com/",
		"https://registry.example.com:8443",
		"https://registry.example.com/proxy/prefix",
		"http://localhost:8080",
		"HTTPS://registry.example.com",
		"https://127.0.0.1:8443",
	}

	for _, endpoint := range endpoints {
		t.Run(endpoint, func(t *testing.T) {
			cfg := validConfig()
			cfg.Endpoint = endpoint
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with endpoint %q = %v, want nil", endpoint, err)
			}
		})
	}
}

// TestConfigValidate_AcceptedPageSize verifies the page-size boundary values.
func TestConfigValidate_AcceptedPageSize(t *testing.T) {
	for _, pageSize := range []int{0, 1, 100, math.MaxInt32} {
		t.Run(fmt.Sprintf("%d", pageSize), func(t *testing.T) {
			cfg := validConfig()
			cfg.PageSize = pageSize
			if err := cfg.Validate(); err != nil {
				t.Errorf("Validate() with pageSize %d = %v, want nil", pageSize, err)
			}

			want := pageSize
			if pageSize == 0 {
				want = defaultPageSize
			}
			if got := cfg.pageSize(); got != want {
				t.Errorf("pageSize() = %d, want %d", got, want)
			}
		})
	}
}
