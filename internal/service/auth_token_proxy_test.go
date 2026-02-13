package service_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/auth/authn"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

func TestAuthTokenProxy_ProxyTokenRequest_Retry(t *testing.T) {
	require := require.New(t)
	log := log.InitLogs()

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		if atomic.LoadInt32(&requestCount) < 3 {
			http.Error(w, "server not ready", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(domain.TokenResponse{
			AccessToken: strPtr("test-access-token"),
		})
		require.NoError(err)
	}))
	defer server.Close()

	cfg := &config.Config{
		Auth: &config.Auth{
			OIDC: &domain.OIDCProviderSpec{
				Issuer:   server.URL,
				ClientId: "test-client",
			},
		},
	}
	multiAuth, err := authn.InitMultiAuth(cfg, log, nil)
	require.NoError(err)

	proxy := service.NewAuthTokenProxy(multiAuth)

	// The test server is configured to fail the first two requests.
	// The proxy should retry and succeed on the third attempt.
	// We need to simulate the OIDC discovery as well.
	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(map[string]string{
				"token_endpoint": server.URL,
			})
			require.NoError(err)
		}
	}))
	defer discoveryServer.Close()

	cfg.Auth.OIDC.Issuer = discoveryServer.URL

	tokenReq := &domain.TokenRequest{
		GrantType: domain.AuthorizationCode,
		ClientId:  "test-client",
		Code:      strPtr("test-code"),
	}

	resp, status := proxy.ProxyTokenRequest(context.Background(), "oidc", tokenReq)

	require.Equal(http.StatusOK, status)
	require.NotNil(resp)
	require.Equal("test-access-token", *resp.AccessToken)
	require.Equal(int32(3), atomic.LoadInt32(&requestCount))
}

func strPtr(s string) *string {
	return &s
}

func TestAuthTokenProxy_ProxyTokenRequest_ContextCancel(t *testing.T) {
	require := require.New(t)
	log := log.InitLogs()

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		time.Sleep(2 * time.Second) // Simulate a slow server
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		Auth: &config.Auth{
			OIDC: &domain.OIDCProviderSpec{
				Issuer:   server.URL,
				ClientId: "test-client",
			},
		},
	}
	multiAuth, err := authn.InitMultiAuth(cfg, log, nil)
	require.NoError(err)

	proxy := service.NewAuthTokenProxy(multiAuth)

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(map[string]string{
				"token_endpoint": server.URL,
			})
			require.NoError(err)
		}
	}))
	defer discoveryServer.Close()

	cfg.Auth.OIDC.Issuer = discoveryServer.URL

	tokenReq := &domain.TokenRequest{
		GrantType: domain.AuthorizationCode,
		ClientId:  "test-client",
		Code:      strPtr("test-code"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, status := proxy.ProxyTokenRequest(ctx, "oidc", tokenReq)

	require.Equal(http.StatusBadRequest, status)
	require.Equal(int32(1), atomic.LoadInt32(&requestCount)) // Should only make one attempt before context cancels
}

func TestAuthTokenProxy_ProxyTokenRequest_NoRetryOnSuccess(t *testing.T) {
	require := require.New(t)
	log := log.InitLogs()

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.Header().Set("Content-Type", "application/json")
		err := json.NewEncoder(w).Encode(domain.TokenResponse{
			AccessToken: strPtr("test-access-token"),
		})
		require.NoError(err)
	}))
	defer server.Close()

	cfg := &config.Config{
		Auth: &config.Auth{
			OIDC: &domain.OIDCProviderSpec{
				Issuer:   server.URL,
				ClientId: "test-client",
			},
		},
	}
	multiAuth, err := authn.InitMultiAuth(cfg, log, nil)
	require.NoError(err)

	proxy := service.NewAuthTokenProxy(multiAuth)

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(map[string]string{
				"token_endpoint": server.URL,
			})
			require.NoError(err)
		}
	}))
	defer discoveryServer.Close()

	cfg.Auth.OIDC.Issuer = discoveryServer.URL

	tokenReq := &domain.TokenRequest{
		GrantType: domain.AuthorizationCode,
		ClientId:  "test-client",
		Code:      strPtr("test-code"),
	}

	resp, status := proxy.ProxyTokenRequest(context.Background(), "oidc", tokenReq)

	require.Equal(http.StatusOK, status)
	require.NotNil(resp)
	require.Equal("test-access-token", *resp.AccessToken)
	require.Equal(int32(1), atomic.LoadInt32(&requestCount))
}

func TestAuthTokenProxy_ProxyTokenRequest_FailsAfterAllRetries(t *testing.T) {
	require := require.New(t)
	log := log.InitLogs()

	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := &config.Config{
		Auth: &config.Auth{
			OIDC: &domain.OIDCProviderSpec{
				Issuer:   server.URL,
				ClientId: "test-client",
			},
		},
	}
	multiAuth, err := authn.InitMultiAuth(cfg, log, nil)
	require.NoError(err)

	proxy := service.NewAuthTokenProxy(multiAuth)

	discoveryServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			w.Header().Set("Content-Type", "application/json")
			err := json.NewEncoder(w).Encode(map[string]string{
				"token_endpoint": server.URL,
			})
			require.NoError(err)
		}
	}))
	defer discoveryServer.Close()

	cfg.Auth.OIDC.Issuer = discoveryServer.URL

	tokenReq := &domain.TokenRequest{
		GrantType: domain.AuthorizationCode,
		ClientId:  "test-client",
		Code:      strPtr("test-code"),
	}

	resp, status := proxy.ProxyTokenRequest(context.Background(), "oidc", tokenReq)

	require.Equal(http.StatusBadRequest, status)
	require.NotNil(resp)
	require.Equal("server_error", *resp.Error)
	require.Equal(int32(5), atomic.LoadInt32(&requestCount))
}
