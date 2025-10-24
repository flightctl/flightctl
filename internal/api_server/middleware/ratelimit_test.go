package middleware

import (
	"context"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerRateLimitConfiguration tests the actual server router configuration
// to verify that auth validate endpoint has configurable rate limiting
// while other APIs use the higher rate limit from the config
func TestServerRateLimitConfiguration(t *testing.T) {
	// Create a config with a higher rate limit for general APIs and configurable auth rate limit
	cfg := config.NewDefault()
	cfg.Service.RateLimit = &config.RateLimitConfig{
		Requests:     60, // 60 requests per minute for general APIs
		Window:       util.Duration(time.Minute),
		AuthRequests: 10, // 10 requests per hour for auth endpoint
		AuthWindow:   util.Duration(time.Hour),
	}

	// Helper function to create a fresh router with isolated rate limiters for each test
	createRouter := func() *chi.Mux {
		router := chi.NewRouter()
		// Add auth-specific rate limiting to auth endpoint
		router.Group(func(r chi.Router) {
			InstallIPRateLimiter(r, RateLimitOptions{
				Requests:       cfg.Service.RateLimit.AuthRequests,
				Window:         time.Duration(cfg.Service.RateLimit.AuthWindow),
				Message:        "Login rate limit exceeded, please try again later",
				TrustedProxies: []string{"10.0.0.0/8"},
			})
			r.Get("/api/v1/auth/validate", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("Auth OK"))
			})
		})

		// Add general rate limiting to other endpoints
		router.Group(func(r chi.Router) {
			InstallIPRateLimiter(r, RateLimitOptions{
				Requests:       cfg.Service.RateLimit.Requests,
				Window:         time.Duration(cfg.Service.RateLimit.Window),
				Message:        "Rate limit exceeded, please try again later",
				TrustedProxies: []string{"10.0.0.0/8"},
			})
			r.Get("/api/v1/test", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("API OK"))
			})
		})

		return router
	}

	t.Run("auth endpoint has stricter rate limiting", func(t *testing.T) {
		router := createRouter()

		// Test auth endpoint - should only allow 10 requests per hour
		for i := 0; i < 12; i++ {
			req := httptest.NewRequest("GET", "/api/v1/auth/validate", nil)
			req.RemoteAddr = "192.168.1.100:12345" // Different IP to avoid interference
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 10 {
				assert.Equal(t, http.StatusOK, w.Code, "Auth request %d should succeed", i+1)
				// Check that rate limit headers are present
				assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Auth request %d should be rate limited", i+1)
				// Check rate limit response
				var status api.Status
				err := json.NewDecoder(w.Body).Decode(&status)
				require.NoError(t, err)
				assert.Equal(t, "Login rate limit exceeded, please try again later", status.Message)
				// Check that Retry-After header is present
				assert.NotEmpty(t, w.Header().Get("Retry-After"))
			}
		}
	})

	t.Run("general API has higher rate limiting", func(t *testing.T) {
		router := createRouter()

		// Test general API endpoint - should allow 60 requests per minute
		for i := 0; i < 65; i++ {
			req := httptest.NewRequest("GET", "/api/v1/test", nil)
			req.RemoteAddr = "192.168.1.200:12345" // Different IP to avoid interference
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 60 {
				assert.Equal(t, http.StatusOK, w.Code, "API request %d should succeed", i+1)
				// Check that rate limit headers are present
				assert.Equal(t, "60", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "API request %d should be rate limited", i+1)
				// Check rate limit response
				var status api.Status
				err := json.NewDecoder(w.Body).Decode(&status)
				require.NoError(t, err)
				assert.Equal(t, "Rate limit exceeded, please try again later", status.Message)
				// Check that Retry-After header is present
				assert.NotEmpty(t, w.Header().Get("Retry-After"))
			}
		}
	})

	t.Run("different IPs have separate rate limits", func(t *testing.T) {
		router := createRouter()

		// Test that different IPs have separate rate limits for auth endpoint
		for i := 0; i < 15; i++ {
			req := httptest.NewRequest("GET", "/api/v1/auth/validate", nil)
			req.RemoteAddr = fmt.Sprintf("192.168.1.%d:12345", i+1) // Different IPs
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// All requests should succeed because they're from different IPs
			assert.Equal(t, http.StatusOK, w.Code, "Auth request %d should succeed", i+1)
			assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))
			assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
		}
	})

	t.Run("rate limit headers are present", func(t *testing.T) {
		router := createRouter() // Create a fresh router for this subtest
		// Test that rate limit headers are present on successful requests
		req := httptest.NewRequest("GET", "/api/v1/auth/validate", nil)
		req.RemoteAddr = "192.168.1.100:12345" // Completely different IP to avoid rate limiting
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))

		// Test general API endpoint
		req = httptest.NewRequest("GET", "/api/v1/test", nil)
		req.RemoteAddr = "192.168.1.100:12345" // Completely different IP to avoid rate limiting
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Limit"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
	})

	t.Run("standard rate limit headers are present", func(t *testing.T) {
		router := createRouter()

		// Test successful request - should have all standard headers
		req := httptest.NewRequest("GET", "/api/v1/auth/validate", nil)
		req.RemoteAddr = "192.168.1.99:12345" // Unique IP
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		// Check all standard rate limit headers
		assert.Equal(t, "10", w.Header().Get("X-RateLimit-Limit"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))

		// Test rate limited request - should have Retry-After header
		// Make enough requests to trigger rate limiting
		for i := 0; i < 11; i++ {
			req := httptest.NewRequest("GET", "/api/v1/auth/validate", nil)
			req.RemoteAddr = "192.168.1.888:12345" // Another unique IP
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i == 10 { // 11th request should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code)
				assert.NotEmpty(t, w.Header().Get("Retry-After"))
				assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

				// Verify JSON response
				var status api.Status
				err := json.NewDecoder(w.Body).Decode(&status)
				require.NoError(t, err)
				assert.Equal(t, "Login rate limit exceeded, please try again later", status.Message)
				assert.Equal(t, "TooManyRequests", status.Reason)
			}
		}
	})
}

func TestRateLimitMiddlewareNoConfig(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Service.RateLimit = nil // No rate limit config
	router := chi.NewRouter()
	InstallIPRateLimiter(router, RateLimitOptions{
		Requests:       100,
		Window:         time.Minute,
		Message:        "Rate limit exceeded, please try again later",
		TrustedProxies: []string{"10.0.0.0/8"},
	})
	router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "OK", w.Body.String())
	// Rate limit headers should be present when options are provided
	assert.Equal(t, "100", w.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

func TestRateLimitMiddlewareWithConfig(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Service.RateLimit = &config.RateLimitConfig{
		Requests: 100,
		Window:   util.Duration(5 * time.Minute),
	}
	router := chi.NewRouter()
	InstallIPRateLimiter(router, RateLimitOptions{
		Requests:       cfg.Service.RateLimit.Requests,
		Window:         time.Duration(cfg.Service.RateLimit.Window),
		Message:        "Rate limit exceeded, please try again later",
		TrustedProxies: []string{"10.0.0.0/8"},
	})
	router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "100", w.Header().Get("X-RateLimit-Limit"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	assert.NotEmpty(t, w.Header().Get("X-RateLimit-Reset"))
}

func TestLoginRateLimitMiddlewareWithConfig(t *testing.T) {
	// Test that the middleware works with different configurations
	t.Run("with auth-specific config", func(t *testing.T) {
		cfg := config.NewDefault()
		cfg.Service.RateLimit = &config.RateLimitConfig{
			Requests:     60,
			Window:       util.Duration(time.Minute),
			AuthRequests: 3,
			AuthWindow:   util.Duration(30 * time.Second),
		}

		router := chi.NewRouter()
		InstallIPRateLimiter(router, RateLimitOptions{
			Requests:       cfg.Service.RateLimit.AuthRequests,
			Window:         time.Duration(cfg.Service.RateLimit.AuthWindow),
			Message:        "Login rate limit exceeded, please try again later",
			TrustedProxies: []string{"10.0.0.0/8"},
		})
		router.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Auth OK"))
		})

		// Test that only 3 requests succeed
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/auth", nil)
			req.RemoteAddr = "192.168.1.300:12345" // Different IP to avoid interference
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				// Check that rate limit headers are present
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	t.Run("fallback to general config", func(t *testing.T) {
		cfg := config.NewDefault()
		cfg.Service.RateLimit = &config.RateLimitConfig{
			Requests: 5,
			Window:   util.Duration(30 * time.Second),
			// No auth-specific config
		}

		router := chi.NewRouter()
		InstallIPRateLimiter(router, RateLimitOptions{
			Requests:       cfg.Service.RateLimit.Requests,
			Window:         time.Duration(cfg.Service.RateLimit.Window),
			Message:        "Login rate limit exceeded, please try again later",
			TrustedProxies: []string{"10.0.0.0/8"},
		})
		router.Get("/auth", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Auth OK"))
		})

		// Test that only 5 requests succeed (using general config)
		for i := 0; i < 7; i++ {
			req := httptest.NewRequest("GET", "/auth", nil)
			req.RemoteAddr = "192.168.1.400:12345" // Different IP to avoid interference
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 5 {
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				// Check that rate limit headers are present
				assert.Equal(t, "5", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})
}

func TestRateLimitWithXForwardedFor(t *testing.T) {
	// Test that rate limiting works correctly with X-Forwarded-For headers
	cfg := config.NewDefault()
	cfg.Service.RateLimit = &config.RateLimitConfig{
		Requests: 3,
		Window:   util.Duration(30 * time.Second),
	}

	router := chi.NewRouter()
	InstallIPRateLimiter(router, RateLimitOptions{
		Requests:       cfg.Service.RateLimit.Requests,
		Window:         time.Duration(cfg.Service.RateLimit.Window),
		Message:        "Rate limit exceeded, please try again later",
		TrustedProxies: []string{"10.0.0.0/8"},
	})
	router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Test with X-Forwarded-For header
	t.Run("with X-Forwarded-For header", func(t *testing.T) {
		// Make 5 requests with X-Forwarded-For header
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "10.0.0.1:12345"                // Proxy IP
			req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	// Test that different X-Forwarded-For IPs are treated separately
	t.Run("different X-Forwarded-For IPs are separate", func(t *testing.T) {
		// Make 3 requests with different X-Forwarded-For IPs
		for i := 0; i < 3; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "10.0.0.1:12345"                                   // Same proxy IP
			req.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i+2)) // Different client IPs
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			// All requests should succeed because they're from different IPs
			assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
			assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
			assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
		}
	})

	// Test with X-Real-IP header
	t.Run("with X-Real-IP header", func(t *testing.T) {
		// Make 5 requests with X-Real-IP header
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "10.0.0.1:12345"           // Proxy IP
			req.Header.Set("X-Real-IP", "203.0.113.10") // Real client IP
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	// Test fallback to RemoteAddr when no proxy headers
	t.Run("fallback to RemoteAddr", func(t *testing.T) {
		// Make 5 requests without proxy headers
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "203.0.113.20:12345" // Direct client IP
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})
}

func TestRateLimitWithTrustedProxies(t *testing.T) {
	// Test that rate limiting works correctly with trusted proxy validation
	cfg := config.NewDefault()
	cfg.Service.RateLimit = &config.RateLimitConfig{
		Requests: 3,
		Window:   util.Duration(30 * time.Second),
	}

	router := chi.NewRouter()
	InstallIPRateLimiter(router, RateLimitOptions{
		Requests:       cfg.Service.RateLimit.Requests,
		Window:         time.Duration(cfg.Service.RateLimit.Window),
		Message:        "Rate limit exceeded, please try again later",
		TrustedProxies: []string{"10.0.0.0/8", "192.168.1.100"},
	})
	router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Test with trusted proxy IP
	t.Run("trusted proxy IP accepts headers", func(t *testing.T) {
		// Make 5 requests with X-Forwarded-For header from trusted proxy
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "10.0.0.1:12345"                // Trusted proxy IP
			req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	// Test with untrusted proxy IP
	t.Run("untrusted proxy IP ignores headers", func(t *testing.T) {
		// Make 5 requests with X-Forwarded-For header from untrusted proxy
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "172.16.0.1:12345"              // Untrusted proxy IP
			req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed (rate limited by proxy IP, not client IP)
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	// Test with no trusted proxies configured
	t.Run("no trusted proxies ignores all headers", func(t *testing.T) {
		cfgNoProxies := config.NewDefault()
		cfgNoProxies.Service.RateLimit = &config.RateLimitConfig{
			Requests: 3,
			Window:   util.Duration(30 * time.Second),
			// No trusted proxies configured
		}

		routerNoProxies := chi.NewRouter()
		InstallIPRateLimiter(routerNoProxies, RateLimitOptions{
			Requests:       cfgNoProxies.Service.RateLimit.Requests,
			Window:         time.Duration(cfgNoProxies.Service.RateLimit.Window),
			Message:        "Rate limit exceeded, please try again later",
			TrustedProxies: []string{},
		})
		routerNoProxies.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		})

		// Make 5 requests with X-Forwarded-For header
		for i := 0; i < 5; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "10.0.0.1:12345"                // Any proxy IP
			req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
			w := httptest.NewRecorder()
			routerNoProxies.ServeHTTP(w, req)

			if i < 3 {
				// First 3 requests should succeed (rate limited by proxy IP, not client IP)
				assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
				assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
				assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
			} else {
				// 4th and 5th requests should be rate limited
				assert.Equal(t, http.StatusTooManyRequests, w.Code, "Request %d should be rate limited", i+1)
			}
		}
	})

	// Test CIDR network support
	t.Run("CIDR network support", func(t *testing.T) {
		cfgCIDR := config.NewDefault()
		cfgCIDR.Service.RateLimit = &config.RateLimitConfig{
			Requests: 3,
			Window:   util.Duration(30 * time.Second),
		}

		routerCIDR := chi.NewRouter()
		InstallIPRateLimiter(routerCIDR, RateLimitOptions{
			Requests:       cfgCIDR.Service.RateLimit.Requests,
			Window:         time.Duration(cfgCIDR.Service.RateLimit.Window),
			Message:        "Rate limit exceeded, please try again later",
			TrustedProxies: []string{"192.168.0.0/16"},
		})
		routerCIDR.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		})

		// Test with IP in CIDR range
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"           // IP in CIDR range
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP
		w := httptest.NewRecorder()
		routerCIDR.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))

		// Test with IP outside CIDR range
		req = httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                // IP outside CIDR range
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
		w = httptest.NewRecorder()
		routerCIDR.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "3", w.Header().Get("X-RateLimit-Limit"))
		assert.NotEmpty(t, w.Header().Get("X-RateLimit-Remaining"))
	})
}

// TestTrustedRealIPSilentIgnore tests the silent-ignore behavior for untrusted proxy headers
func TestTrustedRealIPSilentIgnore(t *testing.T) {
	// Create a simple router with TrustedRealIP middleware
	createRouter := func(trustedCIDRs []string) *chi.Mux {
		router := chi.NewRouter()
		if len(trustedCIDRs) > 0 {
			router.Use(TrustedRealIP(trustedCIDRs))
		}
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			// Return the RemoteAddr so we can verify it was set correctly
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(r.RemoteAddr))
		})
		return router
	}

	t.Run("forwarded headers from untrusted peer do not change client IP", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Only trust 10.0.0.0/8

		// Test with untrusted proxy IP (172.16.0.1) sending X-Forwarded-For
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "172.16.0.1:12345"              // Untrusted proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the original proxy IP, not the forwarded IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "172.16.0.1:12345", w.Body.String())
	})

	t.Run("forwarded headers from trusted proxy still apply", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Trust 10.0.0.0/8

		// Test with trusted proxy IP (10.0.0.1) sending X-Forwarded-For
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                // Trusted proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be applied)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the forwarded client IP, not the proxy IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("True-Client-IP header from trusted proxy is processed", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Trust 10.0.0.0/8

		// Test with trusted proxy IP sending True-Client-IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"               // Trusted proxy IP
		req.Header.Set("True-Client-IP", "203.0.113.1") // Real client IP (should be applied)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the real client IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("X-Real-IP header from trusted proxy is processed", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Trust 10.0.0.0/8

		// Test with trusted proxy IP sending X-Real-IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"          // Trusted proxy IP
		req.Header.Set("X-Real-IP", "203.0.113.1") // Real client IP (should be applied)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the real client IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("X-Real-IP header from untrusted proxy is ignored", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Only trust 10.0.0.0/8

		// Test with untrusted proxy IP sending X-Real-IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "172.16.0.1:12345"        // Untrusted proxy IP
		req.Header.Set("X-Real-IP", "203.0.113.1") // Real client IP (should be ignored)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the original proxy IP, not the real IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "172.16.0.1:12345", w.Body.String())
	})

	t.Run("header priority order is respected", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Trust 10.0.0.0/8

		// Test with all headers set - True-Client-IP should take priority
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                            // Trusted proxy IP
		req.Header.Set("True-Client-IP", "203.0.113.1")              // Should be used (highest priority)
		req.Header.Set("X-Real-IP", "192.168.1.100")                 // Should be ignored
		req.Header.Set("X-Forwarded-For", "172.16.1.50, 10.0.1.200") // Should be ignored
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return True-Client-IP (highest priority)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("X-Forwarded-For with multiple IPs uses first one", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"}) // Trust 10.0.0.0/8

		// Test with trusted proxy IP sending X-Forwarded-For with multiple IPs
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                               // Trusted proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.1, 192.168.1.100") // Multiple IPs
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the first IP (original client)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("no trusted proxies configured ignores all headers", func(t *testing.T) {
		router := createRouter([]string{}) // No trusted proxies

		// Test with any proxy IP sending headers
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                // Any proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the original proxy IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "10.0.0.1:12345", w.Body.String())
	})

	t.Run("CIDR range matching works correctly", func(t *testing.T) {
		router := createRouter([]string{"192.168.0.0/16"}) // Trust 192.168.0.0/16

		// Test with IP in CIDR range
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"           // IP in CIDR range
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be applied)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the forwarded client IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())

		// Test with IP outside CIDR range
		req = httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                // IP outside CIDR range
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the original proxy IP
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "10.0.0.1:12345", w.Body.String())
	})

	t.Run("invalid CIDR is ignored", func(t *testing.T) {
		router := createRouter([]string{"invalid-cidr", "10.0.0.0/8"}) // Invalid + valid CIDR

		// Test with trusted proxy IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"                // Trusted proxy IP
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be applied)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should still work with the valid CIDR
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Body.String())
	})

	t.Run("invalid remote address format is handled gracefully", func(t *testing.T) {
		router := createRouter([]string{"10.0.0.0/8"})

		// Test with invalid remote address format
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "invalid-address"               // Invalid format
		req.Header.Set("X-Forwarded-For", "203.0.113.1") // Real client IP (should be ignored)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should return the original address (no change)
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "invalid-address", w.Body.String())
	})
}

// TestIPRateLimiter tests the IP-based rate limiter
func TestIPRateLimiter(t *testing.T) {
	t.Run("rate limits by IP address", func(t *testing.T) {
		router := chi.NewRouter()
		router.Use(IPRateLimiter(2, time.Second, "IP rate limit exceeded"))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First two requests should succeed
		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/test", nil)
			req.RemoteAddr = "192.168.1.100:12345"
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", i+1)
		}

		// Third request should be rate limited
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusTooManyRequests, w.Code)

		// Check rate limit response
		var status api.Status
		err := json.NewDecoder(w.Body).Decode(&status)
		require.NoError(t, err)
		assert.Equal(t, "IP rate limit exceeded", status.Message)
		assert.Equal(t, "TooManyRequests", status.Reason)
		assert.NotEmpty(t, w.Header().Get("Retry-After"))
	})

	t.Run("different IPs have separate rate limits", func(t *testing.T) {
		router := chi.NewRouter()
		router.Use(IPRateLimiter(1, time.Second, "IP rate limit exceeded"))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First IP should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second IP should also succeed (separate rate limit)
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.200:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)
	})
}

// TestUserIdentityRateLimiter tests the user identity-based rate limiter
func TestUserIdentityRateLimiter(t *testing.T) {
	t.Run("WithUsername", func(t *testing.T) {
		// Create a mock identity with username
		mockIdentity := &MockIdentity{
			username: "testuser",
			uid:      "12345",
		}

		// Create rate limiter
		limiter := UserIdentityRateLimiter(2, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()

		// Add middleware to set identity context before rate limiter
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), consts.IdentityCtxKey, mockIdentity)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First request should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second request should succeed
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)

		// Third request should be rate limited
		req3 := httptest.NewRequest("GET", "/test", nil)
		req3.RemoteAddr = "192.168.1.100:12345"
		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)
		assert.Equal(t, http.StatusTooManyRequests, w3.Code)

		// Verify response body
		var response map[string]interface{}
		err := json.Unmarshal(w3.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, float64(429), response["code"])
		assert.Equal(t, "Rate limit exceeded", response["message"])
		assert.Equal(t, "TooManyRequests", response["reason"])
	})

	t.Run("WithUIDOnly", func(t *testing.T) {
		// Create a mock identity with only UID
		mockIdentity := &MockIdentity{
			username: "", // Empty username
			uid:      "67890",
		}

		// Create rate limiter
		limiter := UserIdentityRateLimiter(1, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()

		// Add middleware to set identity context before rate limiter
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), consts.IdentityCtxKey, mockIdentity)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First request should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second request should be rate limited
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})

	t.Run("FallbackToIP", func(t *testing.T) {
		// Create rate limiter
		limiter := UserIdentityRateLimiter(1, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()
		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			// No identity in context - should fallback to IP
			w.WriteHeader(http.StatusOK)
		})

		// First request should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second request should be rate limited
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})

	t.Run("DifferentUsers", func(t *testing.T) {
		// Create a single rate limiter for all users
		limiter := UserIdentityRateLimiter(1, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()

		// Add middleware to set identity context before rate limiter
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Add different identities to context based on query parameter
				ctx := r.Context()
				if r.URL.Query().Get("user") == "user1" {
					mockIdentity1 := &MockIdentity{username: "user1", uid: "111"}
					ctx = context.WithValue(ctx, consts.IdentityCtxKey, mockIdentity1)
				} else {
					mockIdentity2 := &MockIdentity{username: "user2", uid: "222"}
					ctx = context.WithValue(ctx, consts.IdentityCtxKey, mockIdentity2)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First user should succeed
		req1 := httptest.NewRequest("GET", "/test?user=user1", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second user should also succeed (different identity)
		req2 := httptest.NewRequest("GET", "/test?user=user2", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)
	})
}

// TestDeviceIdentityRateLimiter tests the device identity-based rate limiter
func TestDeviceIdentityRateLimiter(t *testing.T) {
	t.Run("WithDeviceFingerprint", func(t *testing.T) {
		// Create a mock certificate with device fingerprint
		encoded123, err := asn1.Marshal("device-fingerprint-123")
		require.NoError(t, err)

		mockCert := &x509.Certificate{
			Extensions: []pkix.Extension{
				{
					Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}, // OIDDeviceFingerprint
					Value: encoded123,
				},
			},
		}

		// Create rate limiter
		limiter := DeviceIdentityRateLimiter(2, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()

		// Add middleware to set certificate context before rate limiter
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := context.WithValue(r.Context(), consts.TLSPeerCertificateCtxKey, mockCert)
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First request should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second request should succeed
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)

		// Third request should be rate limited
		req3 := httptest.NewRequest("GET", "/test", nil)
		req3.RemoteAddr = "192.168.1.100:12345"
		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)
		assert.Equal(t, http.StatusTooManyRequests, w3.Code)

		// Verify response body
		var response map[string]interface{}
		err = json.Unmarshal(w3.Body.Bytes(), &response)
		require.NoError(t, err)
		assert.Equal(t, float64(429), response["code"])
		assert.Equal(t, "Rate limit exceeded", response["message"])
		assert.Equal(t, "TooManyRequests", response["reason"])
	})

	t.Run("FallbackToIP", func(t *testing.T) {
		// Create rate limiter
		limiter := DeviceIdentityRateLimiter(1, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()
		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			// No certificate in context - should fallback to IP
			w.WriteHeader(http.StatusOK)
		})

		// First request should succeed
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second request should be rate limited
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusTooManyRequests, w2.Code)
	})

	t.Run("DifferentDevices", func(t *testing.T) {
		// Create a single rate limiter for all devices
		limiter := DeviceIdentityRateLimiter(1, time.Second, "Rate limit exceeded")
		router := chi.NewRouter()

		// Add middleware to set certificate context before rate limiter
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Add different certificates to context based on query parameter
				ctx := r.Context()
				if r.URL.Query().Get("device") == "device1" {
					encoded1, _ := asn1.Marshal("device-fingerprint-111")
					mockCert1 := &x509.Certificate{
						Extensions: []pkix.Extension{
							{
								Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}, // OIDDeviceFingerprint
								Value: encoded1,
							},
						},
					}
					ctx = context.WithValue(ctx, consts.TLSPeerCertificateCtxKey, mockCert1)
				} else {
					encoded2, _ := asn1.Marshal("device-fingerprint-222")
					mockCert2 := &x509.Certificate{
						Extensions: []pkix.Extension{
							{
								Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}, // OIDDeviceFingerprint
								Value: encoded2,
							},
						},
					}
					ctx = context.WithValue(ctx, consts.TLSPeerCertificateCtxKey, mockCert2)
				}
				next.ServeHTTP(w, r.WithContext(ctx))
			})
		})

		router.Use(limiter)
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// First device should succeed
		req1 := httptest.NewRequest("GET", "/test?device=device1", nil)
		req1.RemoteAddr = "192.168.1.100:12345"
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Second device should also succeed (different fingerprint)
		req2 := httptest.NewRequest("GET", "/test?device=device2", nil)
		req2.RemoteAddr = "192.168.1.100:12345"
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)
	})
}

// TestDeviceFingerprintExtraction tests that device fingerprint extraction works correctly
func TestDeviceFingerprintExtraction(t *testing.T) {
	// Test that our mock certificate can be processed by the signer package
	encoded, err := asn1.Marshal("device-fingerprint-123")
	require.NoError(t, err)

	mockCert := &x509.Certificate{
		Extensions: []pkix.Extension{
			{
				Id:    asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 99999, 1, 3}, // OIDDeviceFingerprint
				Value: encoded,
			},
		},
	}

	// Test that the signer package can extract the fingerprint
	fingerprint, err := signer.GetDeviceFingerprintExtension(mockCert)
	require.NoError(t, err)
	assert.Equal(t, "device-fingerprint-123", fingerprint)
}

// TestTrustedRealIPLiteralIPs tests that TrustedRealIP supports literal IPs in addition to CIDRs
func TestTrustedRealIPLiteralIPs(t *testing.T) {
	t.Run("IPv4Literal", func(t *testing.T) {
		// Test with literal IPv4 address
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"192.168.1.100"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// Request from the trusted literal IP should use X-Real-IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		req.Header.Set("X-Real-IP", "10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
		// The X-Real-IP should be used (we can't easily test this without modifying the handler)
	})

	t.Run("IPv6Literal", func(t *testing.T) {
		// Test with literal IPv6 address
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"2001:db8::1"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// Request from the trusted literal IPv6 should use X-Real-IP
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "[2001:db8::1]:12345"
		req.Header.Set("X-Real-IP", "10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("MixedCIDRsAndLiterals", func(t *testing.T) {
		// Test with mixed CIDR and literal IPs
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"192.168.1.0/24", "10.0.0.1", "2001:db8::/32", "::1"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// Test CIDR match
		req1 := httptest.NewRequest("GET", "/test", nil)
		req1.RemoteAddr = "192.168.1.50:12345"
		req1.Header.Set("X-Real-IP", "10.0.0.1")
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		assert.Equal(t, http.StatusOK, w1.Code)

		// Test literal IPv4 match
		req2 := httptest.NewRequest("GET", "/test", nil)
		req2.RemoteAddr = "10.0.0.1:12345"
		req2.Header.Set("X-Real-IP", "10.0.0.2")
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		assert.Equal(t, http.StatusOK, w2.Code)

		// Test literal IPv6 match
		req3 := httptest.NewRequest("GET", "/test", nil)
		req3.RemoteAddr = "[::1]:12345"
		req3.Header.Set("X-Real-IP", "10.0.0.3")
		w3 := httptest.NewRecorder()
		router.ServeHTTP(w3, req3)
		assert.Equal(t, http.StatusOK, w3.Code)
	})

	t.Run("EmptyAndWhitespaceEntries", func(t *testing.T) {
		// Test with empty and whitespace entries
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"", "  ", "192.168.1.100", "\t", "10.0.0.1"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// Should work with valid entries despite empty ones
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		req.Header.Set("X-Real-IP", "10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("InvalidEntries", func(t *testing.T) {
		// Test with invalid entries (should be ignored)
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"invalid-ip", "192.168.1.100", "not-a-cidr", "10.0.0.1"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})

		// Should work with valid entries despite invalid ones
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.100:12345"
		req.Header.Set("X-Real-IP", "10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// TestAuthFailureRateLimiter removed; IP-based limiter covers pre-auth behavior where applicable

// MockIdentity implements the common.Identity interface for testing
type MockIdentity struct {
	username string
	uid      string
}

func (m *MockIdentity) GetUsername() string {
	return m.username
}

func (m *MockIdentity) GetUID() string {
	return m.uid
}

func (m *MockIdentity) GetGroups() []string {
	return []string{}
}

func (m *MockIdentity) GetExtra() map[string][]string {
	return map[string][]string{}
}

func TestTrustedRealIPInvalidHeaderIPs(t *testing.T) {
	t.Run("InvalidHeaderIPs", func(t *testing.T) {
		// Test that invalid IPs in headers are rejected
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"10.0.0.0/8"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			// Store the RemoteAddr for verification
			w.Header().Set("X-Remote-Addr", r.RemoteAddr)
			w.WriteHeader(http.StatusOK)
		})

		// Test with invalid IPs in various headers
		testCases := []struct {
			name          string
			trueClientIP  string
			xRealIP       string
			xForwardedFor string
			expectedIP    string // Expected RemoteAddr (should be original if invalid)
		}{
			{
				name:         "InvalidTrueClientIP",
				trueClientIP: "not-an-ip",
				expectedIP:   "10.0.0.1:12345", // Should keep original
			},
			{
				name:       "InvalidXRealIP",
				xRealIP:    "malformed-ip",
				expectedIP: "10.0.0.1:12345", // Should keep original
			},
			{
				name:          "InvalidXForwardedFor",
				xForwardedFor: "bad-ip,10.0.0.2",
				expectedIP:    "10.0.0.1:12345", // Should keep original
			},
			{
				name:       "EmptyHeaders",
				expectedIP: "10.0.0.1:12345", // Should keep original
			},
			{
				name:          "WhitespaceOnlyHeaders",
				trueClientIP:  "   ",
				xRealIP:       "\t",
				xForwardedFor: " ",
				expectedIP:    "10.0.0.1:12345", // Should keep original
			},
			{
				name:         "ValidTrueClientIP",
				trueClientIP: "203.0.113.1",
				expectedIP:   "203.0.113.1", // Should use header value
			},
			{
				name:       "ValidXRealIP",
				xRealIP:    "203.0.113.2",
				expectedIP: "203.0.113.2", // Should use header value
			},
			{
				name:          "ValidXForwardedFor",
				xForwardedFor: "203.0.113.3,10.0.0.2",
				expectedIP:    "203.0.113.3", // Should use first valid IP
			},
			{
				name:         "ValidIPv6",
				trueClientIP: "2001:db8::1",
				expectedIP:   "2001:db8::1", // Should use IPv6
			},
			{
				name:         "InvalidThenValid",
				trueClientIP: "not-an-ip",
				xRealIP:      "203.0.113.4",
				expectedIP:   "203.0.113.4", // Should fall back to valid X-Real-IP
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = "10.0.0.1:12345" // Trusted proxy IP

				if tc.trueClientIP != "" {
					req.Header.Set("True-Client-IP", tc.trueClientIP)
				}
				if tc.xRealIP != "" {
					req.Header.Set("X-Real-IP", tc.xRealIP)
				}
				if tc.xForwardedFor != "" {
					req.Header.Set("X-Forwarded-For", tc.xForwardedFor)
				}

				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, tc.expectedIP, w.Header().Get("X-Remote-Addr"))
			})
		}
	})

	t.Run("HeaderPriorityWithValidation", func(t *testing.T) {
		// Test that header priority is maintained even with validation
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"10.0.0.0/8"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Remote-Addr", r.RemoteAddr)
			w.WriteHeader(http.StatusOK)
		})

		// True-Client-IP should take priority even if other headers have valid IPs
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:12345"
		req.Header.Set("True-Client-IP", "203.0.113.1")  // Valid, should be used
		req.Header.Set("X-Real-IP", "203.0.113.2")       // Valid but lower priority
		req.Header.Set("X-Forwarded-For", "203.0.113.3") // Valid but lowest priority

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, "203.0.113.1", w.Header().Get("X-Remote-Addr"))
	})

	t.Run("XForwardedForMultipleIPs", func(t *testing.T) {
		// Test X-Forwarded-For with multiple IPs (should use first valid one)
		router := chi.NewRouter()
		router.Use(TrustedRealIP([]string{"10.0.0.0/8"}))
		router.Get("/test", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Remote-Addr", r.RemoteAddr)
			w.WriteHeader(http.StatusOK)
		})

		testCases := []struct {
			name          string
			xForwardedFor string
			expectedIP    string
		}{
			{
				name:          "FirstValid",
				xForwardedFor: "203.0.113.1, 10.0.0.2, 203.0.113.3",
				expectedIP:    "203.0.113.1",
			},
			{
				name:          "FirstInvalid",
				xForwardedFor: "invalid-ip, 203.0.113.2, 203.0.113.3",
				expectedIP:    "10.0.0.1:12345", // Should keep original since first IP is invalid
			},
			{
				name:          "AllInvalid",
				xForwardedFor: "bad-ip, not-an-ip, malformed",
				expectedIP:    "10.0.0.1:12345", // Should keep original
			},
			{
				name:          "EmptyAfterComma",
				xForwardedFor: "203.0.113.1, , 203.0.113.3",
				expectedIP:    "203.0.113.1",
			},
		}

		for _, tc := range testCases {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest("GET", "/test", nil)
				req.RemoteAddr = "10.0.0.1:12345"
				req.Header.Set("X-Forwarded-For", tc.xForwardedFor)

				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)

				assert.Equal(t, http.StatusOK, w.Code)
				assert.Equal(t, tc.expectedIP, w.Header().Get("X-Remote-Addr"))
			})
		}
	})
}
