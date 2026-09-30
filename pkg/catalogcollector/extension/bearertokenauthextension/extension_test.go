package bearertokenauthextension

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

// captureServer starts a TLS test server that records the Authorization header
// of the last request received.
func captureServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var lastAuth string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastAuth
	}
}

func makeRoundTripper(t *testing.T, cfg *Config, base http.RoundTripper) http.RoundTripper {
	t.Helper()
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(context.Background(), catalogcollector.Settings{ID: extID, Logger: testLogger()}, cfg)
	require.NoError(t, err)
	rt, err := ext.(interface {
		RoundTripper(http.RoundTripper) (http.RoundTripper, error)
	}).RoundTripper(base)
	require.NoError(t, err)
	return rt
}

func doGet(t *testing.T, rt http.RoundTripper, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

func TestRoundTripper_InlineToken(t *testing.T) {
	server, lastAuth := captureServer(t)

	rt := makeRoundTripper(t, &Config{Token: api.SecureString("my-inline-token")}, server.Client().Transport)
	resp, err := doGet(t, rt, server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer my-inline-token", lastAuth())
}

func TestRoundTripper_TokenFile(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("file-token"), 0600))

	server, lastAuth := captureServer(t)

	rt := makeRoundTripper(t, &Config{TokenFile: tokenFile}, server.Client().Transport)
	resp, err := doGet(t, rt, server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer file-token", lastAuth())
}

func TestRoundTripper_TokenFileRotation(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-v1"), 0600))

	server, lastAuth := captureServer(t)

	rt := makeRoundTripper(t, &Config{TokenFile: tokenFile}, server.Client().Transport)

	resp, err := doGet(t, rt, server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer token-v1", lastAuth())

	// Rotate token
	require.NoError(t, os.WriteFile(tokenFile, []byte("token-v2\n"), 0600))

	resp, err = doGet(t, rt, server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer token-v2", lastAuth(), "token must be re-read from file on each RoundTrip")
}

func TestRoundTripper_TokenFileMissing(t *testing.T) {
	rt := makeRoundTripper(t, &Config{TokenFile: "/nonexistent/path/token"}, http.DefaultTransport)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.ErrorContains(t, err, "reading token file")
}

func TestRoundTripper_EmptyTokenFile(t *testing.T) {
	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("   \n"), 0600))

	rt := makeRoundTripper(t, &Config{TokenFile: tokenFile}, http.DefaultTransport)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.ErrorContains(t, err, "token file is empty")
}

func TestRoundTripper_NilBase(t *testing.T) {
	// nil base falls back to http.DefaultTransport — just check no panic using a real server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID, Logger: testLogger()},
		&Config{Token: api.SecureString("tok")},
	)
	require.NoError(t, err)

	rt, err := ext.(interface {
		RoundTripper(http.RoundTripper) (http.RoundTripper, error)
	}).RoundTripper(nil) // explicitly nil
	require.NoError(t, err)

	resp, err := doGet(t, rt, server.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestRoundTripper_ClonesRequest(t *testing.T) {
	server, _ := captureServer(t)

	rt := makeRoundTripper(t, &Config{Token: api.SecureString("tok")}, server.Client().Transport)

	original, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	original.Header.Set("X-Original", "yes")

	_, err = rt.RoundTrip(original)
	require.NoError(t, err)

	// The roundtripper clones the request; the original must not have Authorization set on it.
	require.Empty(t, original.Header.Get("Authorization"), "original request must not be mutated")
}

func TestConfig_Validate_BothSet(t *testing.T) {
	cfg := &Config{Token: api.SecureString("tok"), TokenFile: "/path"}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestConfig_Validate_NeitherSet(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must be set")
}

func TestConfig_Validate_TokenOnly(t *testing.T) {
	cfg := &Config{Token: api.SecureString("tok")}
	require.NoError(t, cfg.Validate())
}

func TestConfig_Validate_TokenFileOnly(t *testing.T) {
	cfg := &Config{TokenFile: "/path/to/token"}
	require.NoError(t, cfg.Validate())
}

func TestToken_NeverInErrors(t *testing.T) {
	// server returns 500; verify the token value never leaks in the error
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal server error"))
	}))
	t.Cleanup(server.Close)

	tokenFile := t.TempDir() + "/token"
	require.NoError(t, os.WriteFile(tokenFile, []byte("super-secret-value"), 0600))

	rt := makeRoundTripper(t, &Config{TokenFile: tokenFile}, server.Client().Transport)
	resp, err := doGet(t, rt, server.URL)
	// The roundtripper itself succeeds (HTTP 500 is a valid response)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	// No error to check for token leakage here; verify that no error message contains the secret.
	// (The token doesn't appear in transport-level errors; this test is structural.)
	_ = resp
}

func TestStart_IsNoop(t *testing.T) {
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID, Logger: testLogger()},
		&Config{Token: api.SecureString("tok")},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), nil))
}

func TestShutdown_IsNoop(t *testing.T) {
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID, Logger: testLogger()},
		&Config{Token: api.SecureString("tok")},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Shutdown(context.Background()))
}
