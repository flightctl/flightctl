package oauth2clientauthextension

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testLogger() *logrus.Entry {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log.WithField("test", true)
}

// tokenResponse is the JSON body returned by the fake token endpoint.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// parseCredentials extracts client_id and client_secret from a token request,
// handling both HTTP Basic Auth (AuthStyleInHeader) and form-body encoding
// (AuthStyleInParams). AuthStyleAutoDetect tries Basic Auth first, so callers
// should not assume credentials arrive in a fixed location.
func parseCredentials(r *http.Request) (clientID, clientSecret string) {
	if id, secret, ok := r.BasicAuth(); ok {
		return id, secret
	}
	return r.FormValue("client_id"), r.FormValue("client_secret")
}

// tokenServer starts a fake OAuth2 token endpoint that validates grant_type and
// calls the optional onRequest hook before writing the response.
func tokenServer(t *testing.T, expiresIn int, onRequest func(r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		if onRequest != nil {
			onRequest(r)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "test-token-abc",
			TokenType:   "Bearer",
			ExpiresIn:   expiresIn,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// apiServer starts a plain HTTP server that records the last Authorization header.
func apiServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var mu sync.Mutex
	var lastAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastAuth = r.Header.Get("Authorization")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() string {
		mu.Lock()
		defer mu.Unlock()
		return lastAuth
	}
}

// makeExtension constructs an extension directly (bypassing factory Validate).
func makeExtension(t *testing.T, cfg *Config) *extension {
	t.Helper()
	ext, err := newExtension(context.Background(), cfg, testLogger())
	require.NoError(t, err)
	return ext
}

func makeRoundTripper(t *testing.T, cfg *Config, base http.RoundTripper) http.RoundTripper {
	t.Helper()
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(context.Background(), catalogcollector.Settings{ID: extID, Logger: testLogger()}, cfg)
	require.NoError(t, err)
	rt, err := ext.(*extension).RoundTripper(base)
	require.NoError(t, err)
	return rt
}

func doGet(t *testing.T, rt http.RoundTripper, rawURL string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	return rt.RoundTrip(req)
}

func zeroDuration() *util.Duration {
	d := util.Duration(0)
	return &d
}

// ---------------------------------------------------------------------------
// Config.Validate tests
// ---------------------------------------------------------------------------

func TestConfig_Validate_MissingClientID(t *testing.T) {
	cfg := &Config{
		ClientSecret: api.SecureString("secret"),
		TokenURL:     "http://token",
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must be set")
}

func TestConfig_Validate_BothClientID(t *testing.T) {
	cfg := &Config{
		ClientID:     "id",
		ClientIDFile: "/path",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     "http://token",
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestConfig_Validate_MissingClientSecret(t *testing.T) {
	cfg := &Config{
		ClientID: "id",
		TokenURL: "http://token",
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must be set")
}

func TestConfig_Validate_BothClientSecret(t *testing.T) {
	cfg := &Config{
		ClientID:         "id",
		ClientSecret:     api.SecureString("secret"),
		ClientSecretFile: "/path",
		TokenURL:         "http://token",
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestConfig_Validate_MissingTokenURL(t *testing.T) {
	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
	}
	err := cfg.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "tokenUrl")
}

func TestConfig_Validate_Valid(t *testing.T) {
	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     "http://token",
	}
	require.NoError(t, cfg.Validate())
}

func TestConfig_Defaults(t *testing.T) {
	cfg := &Config{}
	require.Equal(t, defaultTimeout, cfg.timeoutDuration())
	require.Equal(t, defaultExpiryBuffer, cfg.expiryBufferDuration())
}

// ---------------------------------------------------------------------------
// Token acquisition tests
// ---------------------------------------------------------------------------

func TestToken_GrantType(t *testing.T) {
	var gotGrantType string
	srv := tokenServer(t, 3600, func(r *http.Request) {
		gotGrantType = r.FormValue("grant_type")
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "myid",
		ClientSecret: api.SecureString("mysecret"),
		TokenURL:     srv.URL,
	}
	rt := makeRoundTripper(t, cfg, nil)
	resp, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "client_credentials", gotGrantType)
}

func TestToken_ClientCredentials(t *testing.T) {
	var gotID, gotSecret string
	srv := tokenServer(t, 3600, func(r *http.Request) {
		gotID, gotSecret = parseCredentials(r)
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "my-client",
		ClientSecret: api.SecureString("my-secret"),
		TokenURL:     srv.URL,
	}
	rt := makeRoundTripper(t, cfg, nil)
	_, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, "my-client", gotID)
	require.Equal(t, "my-secret", gotSecret)
}

func TestToken_Scopes(t *testing.T) {
	var gotScope string
	srv := tokenServer(t, 3600, func(r *http.Request) {
		gotScope = r.FormValue("scope")
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
		Scopes:       []string{"read", "write"},
	}
	rt := makeRoundTripper(t, cfg, nil)
	_, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, "read write", gotScope)
}

func TestToken_EndpointParams(t *testing.T) {
	var gotAudience string
	srv := tokenServer(t, 3600, func(r *http.Request) {
		gotAudience = r.FormValue("audience")
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:       "id",
		ClientSecret:   api.SecureString("secret"),
		TokenURL:       srv.URL,
		EndpointParams: map[string]string{"audience": "myapi"},
	}
	rt := makeRoundTripper(t, cfg, nil)
	_, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, "myapi", gotAudience)
}

func TestToken_BearerOnAPIRequest(t *testing.T) {
	srv := tokenServer(t, 3600, nil)
	api1, lastAuth := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
	}
	rt := makeRoundTripper(t, cfg, nil)
	resp, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer test-token-abc", lastAuth())
}

func TestToken_Reuse(t *testing.T) {
	var callCount atomic.Int64
	srv := tokenServer(t, 3600, func(_ *http.Request) {
		callCount.Add(1)
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
	}
	rt := makeRoundTripper(t, cfg, nil)

	for i := 0; i < 5; i++ {
		_, err := doGet(t, rt, api1.URL)
		require.NoError(t, err)
	}
	require.Equal(t, int64(1), callCount.Load(), "token endpoint should be called only once for a long-lived token")
}

func TestToken_Expiry(t *testing.T) {
	var callCount atomic.Int64
	srv := tokenServer(t, 1 /* expires_in=1s */, func(_ *http.Request) {
		callCount.Add(1)
	})
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
		ExpiryBuffer: zeroDuration(),
	}
	rt := makeRoundTripper(t, cfg, nil)

	_, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, int64(1), callCount.Load())

	// Wait for the token to expire (expires_in=1s, expiryBuffer=0), then
	// poll until a second token fetch occurs instead of using a fixed sleep.
	require.Eventually(t, func() bool {
		_, err := doGet(t, rt, api1.URL)
		return err == nil && callCount.Load() >= 2
	}, 5*time.Second, 200*time.Millisecond, "expired token should trigger a second fetch")
}

func TestToken_EndpointFailure_NoPoison(t *testing.T) {
	// AuthStyleAutoDetect tries up to two auth styles per token acquisition.
	// Both must fail to make the first API request return an error.
	const failFirst = 2
	var callCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		if n <= failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "test-token-abc",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		})
	}))
	t.Cleanup(srv.Close)
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
		ExpiryBuffer: zeroDuration(),
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	// First request fails because both auto-detect attempts return 503.
	_, err = doGet(t, rt, api1.URL)
	require.Error(t, err, "first request should fail when all token endpoint attempts return 503")

	// Second request succeeds: the failure is not cached.
	resp, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestToken_SecretFileRotation(t *testing.T) {
	secretFile := t.TempDir() + "/secret"
	require.NoError(t, os.WriteFile(secretFile, []byte("secret-v1"), 0600))

	var callCount atomic.Int64
	var capturedSecrets []string
	var mu sync.Mutex

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := callCount.Add(1)
		require.NoError(t, r.ParseForm())
		_, secret := parseCredentials(r)
		mu.Lock()
		capturedSecrets = append(capturedSecrets, secret)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: fmt.Sprintf("token-%d", n),
			TokenType:   "Bearer",
			ExpiresIn:   1, // short TTL so the token expires
		})
	}))
	t.Cleanup(srv.Close)
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:         "id",
		ClientSecretFile: secretFile,
		TokenURL:         srv.URL,
		ExpiryBuffer:     zeroDuration(),
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	// First request — acquires token using secret-v1
	_, err = doGet(t, rt, api1.URL)
	require.NoError(t, err)

	// Rotate the secret and poll for a second token fetch instead of sleeping.
	require.NoError(t, os.WriteFile(secretFile, []byte("secret-v2"), 0600))

	require.Eventually(t, func() bool {
		_, err := doGet(t, rt, api1.URL)
		return err == nil && callCount.Load() >= 2
	}, 5*time.Second, 200*time.Millisecond, "rotated secret should trigger a second token fetch")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"secret-v1", "secret-v2"}, capturedSecrets)
}

func TestToken_CustomCA(t *testing.T) {
	// Start a TLS token endpoint.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "tls-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		})
	}))
	t.Cleanup(tlsSrv.Close)

	// Extract the server's self-signed certificate as PEM.
	rawCert := tlsSrv.TLS.Certificates[0].Certificate[0]
	caFile := t.TempDir() + "/ca.pem"
	f, err := os.Create(caFile)
	require.NoError(t, err)
	require.NoError(t, pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: rawCert}))
	require.NoError(t, f.Close())

	api1, lastAuth := apiServer(t)

	cfg := &Config{
		ClientID:             "id",
		ClientSecret:         api.SecureString("secret"),
		TokenURL:             tlsSrv.URL,
		CertificateAuthority: caFile,
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	resp, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "Bearer tls-token", lastAuth())
}

func TestToken_MissingCredentialFile(t *testing.T) {
	srv := tokenServer(t, 3600, nil)

	cfg := &Config{
		ClientID:         "id",
		ClientSecretFile: "/nonexistent/secret",
		TokenURL:         srv.URL,
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.ErrorContains(t, err, "reading client secret file")
}

func TestToken_EmptyCredentialFile(t *testing.T) {
	secretFile := t.TempDir() + "/secret"
	require.NoError(t, os.WriteFile(secretFile, []byte("   \n"), 0600))

	srv := tokenServer(t, 3600, nil)

	cfg := &Config{
		ClientID:         "id",
		ClientSecretFile: secretFile,
		TokenURL:         srv.URL,
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.ErrorContains(t, err, "is empty")
}

func TestToken_NilBase(t *testing.T) {
	srv := tokenServer(t, 3600, nil)
	api1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(api1.Close)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
	}
	ext := makeExtension(t, cfg)

	// Passing nil base falls back to http.DefaultTransport
	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	resp, err := doGet(t, rt, api1.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestToken_Concurrent(t *testing.T) {
	var tokenEndpointCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenEndpointCalls.Add(1)
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokenResponse{
			AccessToken: "concurrent-token",
			TokenType:   "Bearer",
			ExpiresIn:   3600,
		})
	}))
	t.Cleanup(srv.Close)
	api1, _ := apiServer(t)

	cfg := &Config{
		ClientID:     "id",
		ClientSecret: api.SecureString("secret"),
		TokenURL:     srv.URL,
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			resp, err := doGet(t, rt, api1.URL)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
		}()
	}
	wg.Wait()

	require.Equal(t, int64(1), tokenEndpointCalls.Load(), "ReuseTokenSourceWithExpiry must serialize token fetches")
}

func TestToken_SecretsNeverInErrors(t *testing.T) {
	const secretValue = "super-secret-value-xyz"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_client"}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &Config{
		ClientID:     "my-client",
		ClientSecret: api.SecureString(secretValue),
		TokenURL:     srv.URL,
	}
	ext := makeExtension(t, cfg)

	rt, err := ext.RoundTripper(nil)
	require.NoError(t, err)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://localhost", nil)
	require.NoError(t, err)
	_, err = rt.RoundTrip(req)
	require.Error(t, err)
	require.NotContains(t, err.Error(), secretValue, "secret must not appear in error messages")
	require.NotContains(t, err.Error(), "my-client", "client ID must not appear in error messages")
}

func TestStart_IsNoop(t *testing.T) {
	srv := tokenServer(t, 3600, nil)
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID, Logger: testLogger()},
		&Config{
			ClientID:     "id",
			ClientSecret: api.SecureString("secret"),
			TokenURL:     srv.URL,
		},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), nil))
}

func TestShutdown_IsNoop(t *testing.T) {
	srv := tokenServer(t, 3600, nil)
	extID := catalogcollector.ComponentID{Type: Type, Name: "test"}
	ext, err := NewFactory().CreateExtension(
		context.Background(),
		catalogcollector.Settings{ID: extID, Logger: testLogger()},
		&Config{
			ClientID:     "id",
			ClientSecret: api.SecureString("secret"),
			TokenURL:     srv.URL,
		},
	)
	require.NoError(t, err)
	require.NoError(t, ext.Shutdown(context.Background()))
}
