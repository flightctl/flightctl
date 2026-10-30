package service

// Tests: token-proxy contract for expires_in.
//
// Two layers exercised:
//
// 1. proxyTokenRequest (auth_token_proxy.go:287-421) — REAL method on an
//    AuthTokenProxy built with a genuine MultiAuth logger and httpClient.
//    An httptest server simulates the upstream provider. Tests cover JSON
//    and form-encoded responses, seconds-valued lifetimes (31536000, 600),
//    large values, nil/negative, and error responses.  The method's HTTP
//    POST, Content-Type dispatch, json.Unmarshal / strconv.Atoi, and error
//    detection are all exercised on the actual production code path.
//
// 2. ProxyTokenRequest (auth_token_proxy.go:81-120) — REAL upper-level
//    method, exercised through a MultiAuth with a stubAAPMiddleware added
//    as a static provider.  Tests verify that upstream 400 invalid_grant
//    and 500 server_error are normalized to HTTP 400 with error details
//    preserved, and that the upstream was actually reached (request body
//    asserted, not just an early invalid_client return).
//
// NOT exercised: the HTTP transport handler (auth_token.go) or CLI
// transport.  The converter is a verified type-alias pass-through.
//
// NO production code is modified.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------
// Helpers: build a real AuthTokenProxy for testing
// -----------------------------------------------------------------------

// newDiagProxy builds an AuthTokenProxy with a real MultiAuth (logger+TLS)
// and an httpClient pointed at the given upstream server URL.
func newDiagProxy(t *testing.T, upstreamURL string) *AuthTokenProxy {
	t.Helper()
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)

	multiAuth := authn.NewMultiAuth(nil, &tls.Config{MinVersion: tls.VersionTLS12}, log)
	proxy := &AuthTokenProxy{
		authN:                 multiAuth,
		httpClient:            &http.Client{Timeout: 5 * time.Second},
		discoveryClient:       &http.Client{Timeout: 5 * time.Second},
		tokenEndpointCache:    make(map[string]*cacheEntry),
		tokenEndpointCacheTTL: 5 * time.Minute,
	}
	return proxy
}

// diagProviderConfig returns a ProviderConfig pointing at the given
// upstream token endpoint URL.
func diagProviderConfig(tokenEndpoint string) *ProviderConfig {
	return &ProviderConfig{
		Issuer:        "https://test-issuer.local",
		TokenEndpoint: tokenEndpoint,
		ClientId:      "test-client-id",
		UseBasicAuth:  false,
	}
}

// diagTokenReq builds a minimal domain.TokenRequest for refresh_token grant.
func diagTokenReq() *domain.TokenRequest {
	gt := domain.RefreshToken
	rt := "test-refresh-token"
	return &domain.TokenRequest{
		GrantType:    gt,
		ClientId:     "test-client-id",
		RefreshToken: &rt,
	}
}

// -----------------------------------------------------------------------
// 1. proxyTokenRequest — real lower-level method
// -----------------------------------------------------------------------

func TestDiagRealProxy_WhenUpstreamReturnsJSONSecondsItShouldPreserve(t *testing.T) {
	tests := []struct {
		name      string
		expiresIn int
	}{
		{"When upstream returns 31536000 (1y)", 31536000},
		{"When upstream returns 600 (10min)", 600},
		{"When upstream returns 3600 (1h)", 3600},
		{"When upstream returns 0 (zero)", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var receivedBody string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				receivedBody = string(body)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"access_token":  "test-access",
					"refresh_token": "test-refresh",
					"token_type":    "bearer",
					"expires_in":    tt.expiresIn,
				})
			}))
			defer upstream.Close()

			proxy := newDiagProxy(t, upstream.URL)
			result, err := proxy.proxyTokenRequest(context.Background(),
				diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

			require.NoError(t, err, "proxyTokenRequest should not return error for valid upstream response")
			require.NotNil(t, result)
			require.NotNil(t, result.TokenResponse)
			require.NotNil(t, result.TokenResponse.ExpiresIn,
				"ExpiresIn should be present in parsed response")

			assert.Equal(t, tt.expiresIn, *result.TokenResponse.ExpiresIn,
				"proxyTokenRequest should preserve expires_in=%d as *int seconds", tt.expiresIn)
			assert.Equal(t, http.StatusOK, result.StatusCode)
			assert.Contains(t, receivedBody, "grant_type=refresh_token",
				"Upstream must receive the actual grant_type in the request body")

			t.Logf("REAL proxyTokenRequest: upstream=%d → parsed ExpiresIn=%d, status=%d",
				tt.expiresIn, *result.TokenResponse.ExpiresIn, result.StatusCode)
		})
	}
}

func TestDiagRealProxy_WhenUpstreamReturnsLargeSecondsItShouldPreserve(t *testing.T) {
	// Legacy 1000-year lifetime in seconds — motivated by AAP docs.
	const legacy1000y = 31536000000
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "t", "token_type": "bearer",
			"expires_in": legacy1000y,
		})
	}))
	defer upstream.Close()

	proxy := newDiagProxy(t, upstream.URL)
	result, err := proxy.proxyTokenRequest(context.Background(),
		diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

	require.NoError(t, err)
	require.NotNil(t, result.TokenResponse.ExpiresIn)
	assert.Equal(t, legacy1000y, *result.TokenResponse.ExpiresIn,
		"proxyTokenRequest should preserve large seconds value in *int")
	t.Logf("REAL proxyTokenRequest: legacy 1000y=%d → ExpiresIn=%d", legacy1000y, *result.TokenResponse.ExpiresIn)
}

func TestDiagRealProxy_WhenUpstreamReturnsNegativeExpiresInItShouldPreserve(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "t", "token_type": "bearer", "expires_in": -1,
		})
	}))
	defer upstream.Close()

	proxy := newDiagProxy(t, upstream.URL)
	result, err := proxy.proxyTokenRequest(context.Background(),
		diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

	require.NoError(t, err)
	require.NotNil(t, result.TokenResponse.ExpiresIn)
	assert.Equal(t, -1, *result.TokenResponse.ExpiresIn)
	t.Logf("REAL proxyTokenRequest: negative → ExpiresIn=%d", *result.TokenResponse.ExpiresIn)
}

func TestDiagRealProxy_WhenUpstreamOmitsExpiresInItShouldBeNil(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "t", "token_type": "bearer",
		})
	}))
	defer upstream.Close()

	proxy := newDiagProxy(t, upstream.URL)
	result, err := proxy.proxyTokenRequest(context.Background(),
		diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

	require.NoError(t, err)
	assert.Nil(t, result.TokenResponse.ExpiresIn)
	t.Logf("REAL proxyTokenRequest: omitted → ExpiresIn=nil")
}

func TestDiagRealProxy_WhenUpstreamReturnsFormEncodedItShouldParse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-www-form-urlencoded")
		_, _ = w.Write([]byte("access_token=t&token_type=bearer&expires_in=31536000"))
	}))
	defer upstream.Close()

	proxy := newDiagProxy(t, upstream.URL)
	result, err := proxy.proxyTokenRequest(context.Background(),
		diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

	require.NoError(t, err)
	require.NotNil(t, result.TokenResponse.ExpiresIn)
	assert.Equal(t, 31536000, *result.TokenResponse.ExpiresIn,
		"proxyTokenRequest form path should parse expires_in as int seconds")
	t.Logf("REAL proxyTokenRequest form: 31536000 → ExpiresIn=%d", *result.TokenResponse.ExpiresIn)
}

func TestDiagRealProxy_WhenUpstreamReturnsErrorJSONItShouldPropagateError(t *testing.T) {
	tests := []struct {
		name       string
		errCode    string
		httpStatus int
		label      string
	}{
		{"When upstream 400 invalid_grant", "invalid_grant", 400, "provider-rejected refresh"},
		{"When upstream 500 server_error", "server_error", 500, "provider internal failure"},
		{"When upstream 200 invalid_grant", "invalid_grant", 200, "body-error at 200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.httpStatus)
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"error": tt.errCode, "error_description": tt.label,
				})
			}))
			defer upstream.Close()

			proxy := newDiagProxy(t, upstream.URL)
			result, err := proxy.proxyTokenRequest(context.Background(),
				diagProviderConfig(upstream.URL+"/token"), "test-client-id", "", diagTokenReq())

			require.NoError(t, err, "proxyTokenRequest returns the error in the response, not as an error return")
			require.NotNil(t, result.TokenResponse.Error)
			assert.Equal(t, tt.errCode, *result.TokenResponse.Error)
			require.NotNil(t, result.TokenResponse.ErrorDescription)
			assert.Equal(t, tt.label, *result.TokenResponse.ErrorDescription)
			assert.Equal(t, tt.httpStatus, result.StatusCode,
				"proxyTokenRequest should report the upstream HTTP status")
			assert.Nil(t, result.TokenResponse.ExpiresIn)

			t.Logf("REAL proxyTokenRequest error: upstream %d %s → parsed error=%s, proxyStatus=%d",
				tt.httpStatus, tt.errCode, *result.TokenResponse.Error, result.StatusCode)
		})
	}
}

// -----------------------------------------------------------------------
// 2. ProxyTokenRequest — real upper-level method with stub AAP provider
// -----------------------------------------------------------------------

// stubAAPMiddleware implements common.AuthNMiddleware + GetAapSpec() so
// findProviderForToken recognizes it as an AAP provider.
type stubAAPMiddleware struct {
	spec         domain.AapProviderSpec
	providerName string
}

func (s *stubAAPMiddleware) GetAuthToken(r *http.Request) (string, error)          { return "", nil }
func (s *stubAAPMiddleware) ValidateToken(ctx context.Context, token string) error { return nil }
func (s *stubAAPMiddleware) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	return nil, nil
}
func (s *stubAAPMiddleware) IsEnabled() bool { return true }
func (s *stubAAPMiddleware) GetAuthConfig() (*domain.AuthConfig, error) {
	providers := []domain.AuthProvider{{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(s.providerName)},
	}}
	return &domain.AuthConfig{Providers: &providers}, nil
}
func (s *stubAAPMiddleware) GetAapSpec() domain.AapProviderSpec { return s.spec }

func newDiagProxyWithAAP(t *testing.T, upstreamURL string) (*AuthTokenProxy, string) {
	t.Helper()
	log := logrus.New()
	log.SetLevel(logrus.DebugLevel)
	multiAuth := authn.NewMultiAuth(nil, &tls.Config{MinVersion: tls.VersionTLS12}, log)

	providerName := "test-aap-provider"
	stub := &stubAAPMiddleware{
		spec: domain.AapProviderSpec{
			ClientId: "test-client-id",
			TokenUrl: upstreamURL + "/o/token/",
			ApiUrl:   upstreamURL,
			Scopes:   []string{"read"},
		},
		providerName: providerName,
	}
	multiAuth.AddStaticProvider("aap-issuer", stub)

	proxy := NewAuthTokenProxy(multiAuth)
	return proxy, providerName
}

func TestDiagRealProxyUpper_WhenUpstream400InvalidGrantItShouldNormalizeTo400(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "invalid_grant", "error_description": "refresh token expired",
		})
	}))
	defer upstream.Close()

	proxy, providerName := newDiagProxyWithAAP(t, upstream.URL)

	gt := domain.RefreshToken
	rt := "expired-refresh"
	tokenReq := &domain.TokenRequest{
		GrantType:    gt,
		ClientId:     "test-client-id",
		RefreshToken: &rt,
	}

	resp, httpStatus := proxy.ProxyTokenRequest(context.Background(), providerName, tokenReq)

	// Verify the upstream was actually reached
	require.NotEmpty(t, receivedBody,
		"Upstream must be reached — empty body means early invalid_client return")
	assert.Contains(t, receivedBody, "grant_type=refresh_token",
		"Upstream must receive the actual token request")

	assert.Equal(t, http.StatusBadRequest, httpStatus,
		"ProxyTokenRequest should normalize upstream 400 to HTTP 400")
	require.NotNil(t, resp.Error)
	assert.Equal(t, "invalid_grant", *resp.Error)
	require.NotNil(t, resp.ErrorDescription)
	assert.Equal(t, "refresh token expired", *resp.ErrorDescription)

	t.Logf("REAL ProxyTokenRequest: upstream 400 invalid_grant → HTTP %d, error=%s",
		httpStatus, *resp.Error)
}

func TestDiagRealProxyUpper_WhenUpstream500ServerErrorItShouldNormalizeTo400(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "server_error", "error_description": "internal failure",
		})
	}))
	defer upstream.Close()

	proxy, providerName := newDiagProxyWithAAP(t, upstream.URL)

	gt := domain.RefreshToken
	rt := "some-refresh"
	tokenReq := &domain.TokenRequest{
		GrantType:    gt,
		ClientId:     "test-client-id",
		RefreshToken: &rt,
	}

	resp, httpStatus := proxy.ProxyTokenRequest(context.Background(), providerName, tokenReq)

	require.NotEmpty(t, receivedBody, "Upstream must be reached")
	assert.Contains(t, receivedBody, "grant_type=refresh_token")

	assert.Equal(t, http.StatusBadRequest, httpStatus,
		"ProxyTokenRequest should normalize upstream 500 to HTTP 400")
	require.NotNil(t, resp.Error)
	assert.Equal(t, "server_error", *resp.Error)

	t.Logf("REAL ProxyTokenRequest: upstream 500 server_error → HTTP %d, error=%s",
		httpStatus, *resp.Error)
}

func TestDiagRealProxyUpper_WhenUpstreamReturns200SuccessItShouldReturn200(t *testing.T) {
	var receivedBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "new-access", "refresh_token": "new-refresh",
			"token_type": "bearer", "expires_in": 31536000,
		})
	}))
	defer upstream.Close()

	proxy, providerName := newDiagProxyWithAAP(t, upstream.URL)

	gt := domain.RefreshToken
	rt := "valid-refresh"
	tokenReq := &domain.TokenRequest{
		GrantType:    gt,
		ClientId:     "test-client-id",
		RefreshToken: &rt,
	}

	resp, httpStatus := proxy.ProxyTokenRequest(context.Background(), providerName, tokenReq)

	require.NotEmpty(t, receivedBody, "Upstream must be reached")
	assert.Contains(t, receivedBody, "grant_type=refresh_token")

	assert.Equal(t, http.StatusOK, httpStatus)
	require.NotNil(t, resp.AccessToken)
	assert.Equal(t, "new-access", *resp.AccessToken)
	require.NotNil(t, resp.ExpiresIn)
	assert.Equal(t, 31536000, *resp.ExpiresIn,
		"ProxyTokenRequest should preserve seconds-valued expires_in through full path")

	t.Logf("REAL ProxyTokenRequest: upstream 200 → HTTP %d, ExpiresIn=%d, access=%s",
		httpStatus, *resp.ExpiresIn, *resp.AccessToken)
}
