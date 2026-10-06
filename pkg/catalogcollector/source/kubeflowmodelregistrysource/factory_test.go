package kubeflowmodelregistrysource

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"
	"github.com/sirupsen/logrus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// --- test doubles ----------------------------------------------------------

// stubHost resolves extensions from a fixed map, mimicking the service host.
type stubHost struct {
	extensions map[catalogcollector.ComponentID]catalogcollector.Extension
}

func (h *stubHost) GetExtension(
	id catalogcollector.ComponentID,
) (catalogcollector.Extension, error) {
	extension, ok := h.extensions[id]
	if !ok {
		return nil, fmt.Errorf("no extension registered for ID %q", id)
	}
	return extension, nil
}

func newStubHost(
	t *testing.T,
	id string,
	extension catalogcollector.Extension,
) *stubHost {
	t.Helper()
	parsed, err := catalogcollector.ParseComponentID(id)
	if err != nil {
		t.Fatalf("ParseComponentID(%q): %v", id, err)
	}
	return &stubHost{
		extensions: map[catalogcollector.ComponentID]catalogcollector.Extension{
			parsed: extension,
		},
	}
}

// headerAuthExtension is a minimal authentication extension. The synthetic
// credential never leaves the test process.
type headerAuthExtension struct {
	header string
	value  string
	err    error

	wrapped atomic.Int64
	applied atomic.Int64
}

func (e *headerAuthExtension) Start(
	context.Context,
	catalogcollector.Host,
) error {
	return nil
}

func (e *headerAuthExtension) Shutdown(context.Context) error { return nil }

func (e *headerAuthExtension) RoundTripper(
	base http.RoundTripper,
) (http.RoundTripper, error) {
	if e.err != nil {
		return nil, e.err
	}
	e.wrapped.Add(1)
	if base == nil {
		base = http.DefaultTransport
	}
	return roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		e.applied.Add(1)
		cloned := req.Clone(req.Context())
		cloned.Header.Set(e.header, e.value)
		return base.RoundTrip(cloned)
	}), nil
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// plainExtension implements Extension but not extensionauth.HTTPClient.
type plainExtension struct{}

func (plainExtension) Start(context.Context, catalogcollector.Host) error { return nil }
func (plainExtension) Shutdown(context.Context) error                     { return nil }

var (
	_ extensionauth.HTTPClient   = (*headerAuthExtension)(nil)
	_ catalogcollector.Extension = (*headerAuthExtension)(nil)
	_ catalogcollector.Extension = plainExtension{}
)

// --- helpers ---------------------------------------------------------------

func testSettings(t *testing.T, host catalogcollector.Host) catalogcollector.Settings {
	t.Helper()

	id, err := catalogcollector.ParseComponentID("kubeflowmodelregistry/test")
	if err != nil {
		t.Fatalf("ParseComponentID: %v", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(sdkmetric.NewManualReader()),
	)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	return catalogcollector.Settings{
		ID:            id,
		Host:          host,
		Logger:        testLogger(),
		MeterProvider: provider,
	}
}

// settingsWithLogCapture returns settings whose logger writes into the
// returned buffer so that construction-time log output can be asserted.
func settingsWithLogCapture(
	t *testing.T,
	host catalogcollector.Host,
) (catalogcollector.Settings, *strings.Builder) {
	t.Helper()

	buffer := &strings.Builder{}
	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)
	logger.SetOutput(buffer)
	logger.SetFormatter(&logrus.TextFormatter{DisableColors: true})

	settings := testSettings(t, host)
	settings.Logger = logger.WithField("component_id", settings.ID.String())

	return settings, buffer
}

// writeFile writes content into an isolated per-test temporary directory.
func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// certificatePEM encodes an httptest TLS server certificate as a PEM bundle
// suitable for use as a certificateAuthority file.
func certificatePEM(t *testing.T, server *httptest.Server) string {
	t.Helper()
	certificate := server.Certificate()
	if certificate == nil {
		t.Fatal("httptest TLS server exposed no certificate")
	}
	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certificate.Raw,
	}))
}

func factoryConfig(endpoint string) *Config {
	cfg := NewFactory().CreateDefaultConfig().(*Config)
	cfg.Endpoint = endpoint
	cfg.Catalog = "my-catalog"
	return cfg
}

// --- factory construction --------------------------------------------------

// TestFactory_TypeAndDefaultConfig verifies the factory identity and the
// defaults it hands to the configuration decoder.
func TestFactory_TypeAndDefaultConfig(t *testing.T) {
	factory := NewFactory()

	if factory.Type() != Type {
		t.Errorf("Type() = %q, want %q", factory.Type(), Type)
	}

	cfg, ok := factory.CreateDefaultConfig().(*Config)
	if !ok {
		t.Fatalf("CreateDefaultConfig() returned %T, want *Config", factory.CreateDefaultConfig())
	}
	if err := cfg.Backoff.Validate(); err != nil {
		t.Errorf("default backoff is not valid: %v", err)
	}
	if cfg.InsecureSkipVerify {
		t.Error("insecureSkipVerify must default to false")
	}
	if cfg.CertificateAuthority != "" {
		t.Errorf("certificateAuthority default = %q, want empty", cfg.CertificateAuthority)
	}
}

// TestCreateSource_Succeeds verifies that a valid configuration produces a
// runnable source wired to the supplied consumer.
func TestCreateSource_Succeeds(t *testing.T) {
	created, err := NewFactory().CreateSource(
		context.Background(),
		testSettings(t, nil),
		factoryConfig("https://model-registry.example.com"),
		&fakeConsumer{},
	)
	if err != nil {
		t.Fatalf("CreateSource() error: %v", err)
	}

	src, ok := created.(*source)
	if !ok {
		t.Fatalf("CreateSource() returned %T, want *source", created)
	}
	if src.catalog != "my-catalog" {
		t.Errorf("catalog = %q, want %q", src.catalog, "my-catalog")
	}
	if src.client == nil || src.poller == nil || src.next == nil {
		t.Error("source was not fully wired by the factory")
	}
	// Source metrics are recorded through the poller's collection callback,
	// so construction must install it.
	if src.poller.OnCollect == nil {
		t.Error("factory did not wire the poller's OnCollect callback; source metrics would never be recorded")
	}
	if _, ok := created.(catalogcollector.SourcePreflight); !ok {
		t.Error("created source does not implement SourcePreflight")
	}
}

// TestCreateSource_WiresCollectionMetrics asserts that the callback the
// factory installs on the polling helper records through the real source
// metrics, under the configured source ID.
func TestCreateSource_WiresCollectionMetrics(t *testing.T) {
	id, err := catalogcollector.ParseComponentID("kubeflowmodelregistry/test")
	if err != nil {
		t.Fatalf("ParseComponentID: %v", err)
	}

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	created, err := NewFactory().CreateSource(
		context.Background(),
		catalogcollector.Settings{
			ID:            id,
			Logger:        testLogger(),
			MeterProvider: provider,
		},
		factoryConfig("https://model-registry.example.com"),
		&fakeConsumer{},
	)
	if err != nil {
		t.Fatalf("CreateSource() error: %v", err)
	}

	src, ok := created.(*source)
	if !ok {
		t.Fatalf("CreateSource() returned %T, want *source", created)
	}
	if src.poller.OnCollect == nil {
		t.Fatal("factory did not wire the poller's OnCollect callback")
	}

	// Drive the wired callback exactly as the helper would.
	src.poller.OnCollect(150*time.Millisecond, nil)
	src.poller.OnCollect(50*time.Millisecond, errors.New("connection refused"))

	outcomes := collectionOutcomes(t, reader, id.String())
	if outcomes["success"] != 1 {
		t.Errorf("collections{outcome=success} = %d, want 1 (all: %v)", outcomes["success"], outcomes)
	}
	if outcomes["failure"] != 1 {
		t.Errorf("collections{outcome=failure} = %d, want 1 (all: %v)", outcomes["failure"], outcomes)
	}
	if ts, observed := lastSuccessTimestamp(t, reader, id.String()); !observed || ts == 0 {
		t.Error("the wired callback did not advance the last-success gauge")
	}
}

// TestCreateSource_RejectsInvalidInput covers the construction-time guard
// rails that do not depend on TLS or authentication.
func TestCreateSource_RejectsInvalidInput(t *testing.T) {
	validCfg := factoryConfig("https://model-registry.example.com")

	cases := []struct {
		name     string
		cfg      catalogcollector.ComponentConfig
		settings func(catalogcollector.Settings) catalogcollector.Settings
		next     catalogcollector.Consumer
		wantErr  string
	}{
		{
			name:    "when the config type is wrong it should fail",
			cfg:     &struct{}{},
			next:    &fakeConsumer{},
			wantErr: "unexpected config type",
		},
		{
			name:    "when the config is invalid it should fail",
			cfg:     factoryConfig("not-a-url"),
			next:    &fakeConsumer{},
			wantErr: "endpoint must be an absolute HTTP or HTTPS URL",
		},
		{
			name: "when the logger is nil it should fail",
			cfg:  validCfg,
			settings: func(s catalogcollector.Settings) catalogcollector.Settings {
				s.Logger = nil
				return s
			},
			next:    &fakeConsumer{},
			wantErr: "logger must not be nil",
		},
		{
			name: "when the meter provider is nil it should fail",
			cfg:  validCfg,
			settings: func(s catalogcollector.Settings) catalogcollector.Settings {
				s.MeterProvider = nil
				return s
			},
			next:    &fakeConsumer{},
			wantErr: "meter provider must not be nil",
		},
		{
			name:    "when the downstream consumer is nil it should fail",
			cfg:     validCfg,
			next:    nil,
			wantErr: "downstream consumer must not be nil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := testSettings(t, nil)
			if tc.settings != nil {
				settings = tc.settings(settings)
			}

			_, err := NewFactory().CreateSource(
				context.Background(),
				settings,
				tc.cfg,
				tc.next,
			)
			if err == nil {
				t.Fatalf("CreateSource() = nil error, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			if !strings.Contains(err.Error(), "kubeflowmodelregistry/test") {
				t.Errorf("error %q does not identify the source", err.Error())
			}
		})
	}
}

// --- TLS transport ---------------------------------------------------------

// TestBuildTransport_Defaults verifies the TLS defaults applied when no
// certificate authority and no insecure override are configured.
func TestBuildTransport_Defaults(t *testing.T) {
	transport, err := buildTransport(factoryConfig("https://model-registry.example.com"))
	if err != nil {
		t.Fatalf("buildTransport() error: %v", err)
	}

	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		t.Fatal("buildTransport() produced no TLS configuration")
	}
	if tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %d, want %d", tlsConfig.MinVersion, tls.VersionTLS12)
	}
	if tlsConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify must be false by default")
	}
	if tlsConfig.RootCAs != nil {
		t.Error("RootCAs must stay nil so the system trust store is used")
	}
	if !transport.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 must be enabled")
	}
	if transport.Proxy == nil {
		t.Error("Proxy must be taken from the environment")
	}
}

// TestBuildTransport_InsecureSkipVerify verifies that the development-only
// override reaches the TLS configuration when it is explicitly enabled.
func TestBuildTransport_InsecureSkipVerify(t *testing.T) {
	cfg := factoryConfig("https://model-registry.example.com")
	cfg.InsecureSkipVerify = true

	transport, err := buildTransport(cfg)
	if err != nil {
		t.Fatalf("buildTransport() error: %v", err)
	}
	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify was not applied")
	}
	if transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Error("the minimum TLS version must still be enforced")
	}
}

// TestBuildTransport_CertificateAuthority covers a valid custom CA, a missing
// CA file, and a file that contains no usable PEM certificate.
func TestBuildTransport_CertificateAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	))
	defer server.Close()

	validCA := writeFile(t, "ca.pem", certificatePEM(t, server))
	emptyPEM := writeFile(t, "empty.pem", "not a certificate\n")
	malformedPEM := writeFile(
		t,
		"malformed.pem",
		"-----BEGIN CERTIFICATE-----\nbm90LWEtY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----\n",
	)
	missingCA := filepath.Join(t.TempDir(), "absent.pem")

	cases := []struct {
		name    string
		path    string
		wantErr string
	}{
		{name: "when the CA file is valid it should be trusted", path: validCA},
		{
			name:    "when the CA file is missing it should fail",
			path:    missingCA,
			wantErr: "reading CA certificate",
		},
		{
			name:    "when the file holds no PEM block it should fail",
			path:    emptyPEM,
			wantErr: "no valid PEM certificates",
		},
		{
			name:    "when the PEM block is malformed it should fail",
			path:    malformedPEM,
			wantErr: "no valid PEM certificates",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := factoryConfig("https://model-registry.example.com")
			cfg.CertificateAuthority = tc.path

			transport, err := buildTransport(cfg)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("buildTransport() = nil error, want %q", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("buildTransport() error: %v", err)
			}
			if transport.TLSClientConfig.RootCAs == nil {
				t.Error("the configured certificate authority was not installed")
			}
		})
	}
}

// TestCreateSource_CertificateAuthorityError verifies that a CA failure is
// reported as a transport-construction error naming the source.
func TestCreateSource_CertificateAuthorityError(t *testing.T) {
	cfg := factoryConfig("https://model-registry.example.com")
	cfg.CertificateAuthority = filepath.Join(t.TempDir(), "absent.pem")

	_, err := NewFactory().CreateSource(
		context.Background(),
		testSettings(t, nil),
		cfg,
		&fakeConsumer{},
	)
	if err == nil {
		t.Fatal("CreateSource() = nil error, want a transport error")
	}
	for _, want := range []string{
		"kubeflowmodelregistry/test",
		"building HTTP transport",
		"reading CA certificate",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err.Error(), want)
		}
	}
}

// --- authentication --------------------------------------------------------

// TestCreateSource_AuthenticationErrors covers every failure mode of
// authenticator resolution and transport wrapping.
func TestCreateSource_AuthenticationErrors(t *testing.T) {
	wrapperErr := errors.New("synthetic wrapper failure")

	cases := []struct {
		name     string
		host     catalogcollector.Host
		wantErrs []string
	}{
		{
			name:     "when no host is available it should fail",
			host:     nil,
			wantErrs: []string{"requires a host but none is available"},
		},
		{
			name:     "when the authenticator is not registered it should fail",
			host:     newStubHost(t, "bearertokenauth/other", &headerAuthExtension{}),
			wantErrs: []string{"resolving authenticator", "bearertokenauth/mr"},
		},
		{
			name:     "when the extension cannot authenticate it should fail",
			host:     newStubHost(t, "bearertokenauth/mr", plainExtension{}),
			wantErrs: []string{"does not implement extensionauth.HTTPClient"},
		},
		{
			name: "when the transport wrapper fails it should fail",
			host: newStubHost(t, "bearertokenauth/mr", &headerAuthExtension{err: wrapperErr}),
			wantErrs: []string{
				"building transport for authenticator",
				wrapperErr.Error(),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := factoryConfig("https://model-registry.example.com")
			cfg.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}

			_, err := NewFactory().CreateSource(
				context.Background(),
				testSettings(t, tc.host),
				cfg,
				&fakeConsumer{},
			)
			if err == nil {
				t.Fatalf("CreateSource() = nil error, want %v", tc.wantErrs)
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
			if !strings.Contains(err.Error(), "kubeflowmodelregistry/test") {
				t.Errorf("error %q does not identify the source", err.Error())
			}
		})
	}
}

// TestCreateSource_NoAuthLeavesTransportUnwrapped verifies that omitting auth
// does not consult the host at all.
func TestCreateSource_NoAuthLeavesTransportUnwrapped(t *testing.T) {
	auth := &headerAuthExtension{header: "Authorization", value: "Bearer unused"}

	_, err := NewFactory().CreateSource(
		context.Background(),
		testSettings(t, newStubHost(t, "bearertokenauth/mr", auth)),
		factoryConfig("https://model-registry.example.com"),
		&fakeConsumer{},
	)
	if err != nil {
		t.Fatalf("CreateSource() error: %v", err)
	}
	if n := auth.wrapped.Load(); n != 0 {
		t.Errorf("authenticator was wrapped %d times, want 0", n)
	}
}

// TestCreateSource_UsesConfiguredCAAndAuthentication drives a real TLS server
// through the factory-created client.
//
// The server certificate is signed by an authority that is not in the system
// trust store, so a successful preflight proves the configured
// certificateAuthority was installed. The handler rejects requests that do not
// carry the synthetic credential, so a successful preflight also proves the
// authentication transport was layered on top of the TLS transport.
func TestCreateSource_UsesConfiguredCAAndAuthentication(t *testing.T) {
	const authHeader = "Authorization"
	const authValue = "Bearer synthetic-test-credential"

	var authorizedRequests atomic.Int64
	var unauthorizedRequests atomic.Int64

	server := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get(authHeader) != authValue {
				unauthorizedRequests.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			authorizedRequests.Add(1)

			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"items":         []any{},
				"size":          0,
				"pageSize":      1,
				"nextPageToken": "",
			}); err != nil {
				t.Errorf("encoding response: %v", err)
			}
		},
	))
	defer server.Close()

	auth := &headerAuthExtension{header: authHeader, value: authValue}

	cfg := factoryConfig(server.URL)
	cfg.CertificateAuthority = writeFile(t, "ca.pem", certificatePEM(t, server))
	cfg.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}

	created, err := NewFactory().CreateSource(
		context.Background(),
		testSettings(t, newStubHost(t, "bearertokenauth/mr", auth)),
		cfg,
		&fakeConsumer{},
	)
	if err != nil {
		t.Fatalf("CreateSource() error: %v", err)
	}

	preflight, ok := created.(catalogcollector.SourcePreflight)
	if !ok {
		t.Fatal("created source does not implement SourcePreflight")
	}
	if err := preflight.Preflight(context.Background()); err != nil {
		t.Fatalf("Preflight() through the configured CA and authenticator: %v", err)
	}

	if n := auth.wrapped.Load(); n != 1 {
		t.Errorf("authenticator transport was built %d times, want exactly 1", n)
	}
	if n := auth.applied.Load(); n < 2 {
		t.Errorf("authentication was applied to %d requests, want at least 2", n)
	}
	if n := unauthorizedRequests.Load(); n != 0 {
		t.Errorf("%d request(s) reached the server without authentication", n)
	}
	if n := authorizedRequests.Load(); n < 2 {
		t.Errorf("server saw %d authenticated requests, want at least 2", n)
	}
}

// TestCreateSource_UntrustedCertificateAuthorityFailsPreflight is the negative
// control for the previous test: without the configured CA, the same TLS
// server must not be trusted.
func TestCreateSource_UntrustedCertificateAuthorityFailsPreflight(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"items": []any{}}); err != nil {
				t.Errorf("encoding response: %v", err)
			}
		},
	))
	defer server.Close()

	created, err := NewFactory().CreateSource(
		context.Background(),
		testSettings(t, nil),
		factoryConfig(server.URL),
		&fakeConsumer{},
	)
	if err != nil {
		t.Fatalf("CreateSource() error: %v", err)
	}

	err = created.(catalogcollector.SourcePreflight).Preflight(context.Background())
	if err == nil {
		t.Fatal("Preflight() succeeded against an untrusted certificate authority")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("error %q does not report a certificate problem", err.Error())
	}
}

// --- insecureSkipVerify warning --------------------------------------------

// TestCreateSource_InsecureSkipVerifyWarning verifies that enabling the
// development-only option logs exactly one warning, that the warning is absent
// otherwise, and that no credential material is written to the log.
func TestCreateSource_InsecureSkipVerifyWarning(t *testing.T) {
	const credential = "Bearer synthetic-test-credential"

	cases := []struct {
		name        string
		insecure    bool
		withAuth    bool
		wantWarning bool
	}{
		{
			name:        "when insecureSkipVerify is disabled it should not warn",
			insecure:    false,
			wantWarning: false,
		},
		{
			name:        "when insecureSkipVerify is enabled it should warn once",
			insecure:    true,
			wantWarning: true,
		},
		{
			name:        "when insecureSkipVerify is combined with auth it should still be allowed",
			insecure:    true,
			withAuth:    true,
			wantWarning: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var host catalogcollector.Host
			cfg := factoryConfig("https://model-registry.example.com")
			cfg.InsecureSkipVerify = tc.insecure
			if tc.withAuth {
				cfg.Auth = &AuthConfig{Authenticator: "bearertokenauth/mr"}
				host = newStubHost(t, "bearertokenauth/mr", &headerAuthExtension{
					header: "Authorization",
					value:  credential,
				})
			}

			settings, logOutput := settingsWithLogCapture(t, host)

			created, err := NewFactory().CreateSource(
				context.Background(),
				settings,
				cfg,
				&fakeConsumer{},
			)
			if err != nil {
				t.Fatalf("CreateSource() error: %v", err)
			}
			if created == nil {
				t.Fatal("CreateSource() returned no source")
			}

			logged := logOutput.String()
			warnings := strings.Count(logged, "insecureSkipVerify is enabled")

			switch {
			case tc.wantWarning && warnings != 1:
				t.Errorf("warning appeared %d times, want exactly 1; log:\n%s", warnings, logged)
			case !tc.wantWarning && warnings != 0:
				t.Errorf("unexpected warning; log:\n%s", logged)
			}

			if tc.wantWarning && !strings.Contains(logged, "level=warning") {
				t.Errorf("message was not logged at warning level; log:\n%s", logged)
			}

			// The warning must never reveal credential material.
			for _, secret := range []string{credential, "synthetic-test-credential", "Bearer "} {
				if strings.Contains(logged, secret) {
					t.Errorf("log leaked credential material %q; log:\n%s", secret, logged)
				}
			}
		})
	}
}
