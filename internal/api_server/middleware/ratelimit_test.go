package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	apiconfig "github.com/flightctl/flightctl/internal/config/api"
	"github.com/flightctl/flightctl/internal/config/common"
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
	cfg := apiconfig.NewDefault()
	cfg.Service.RateLimit = &common.RateLimitConfig{
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
			InstallRateLimiter(r, RateLimitOptions{
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
			InstallRateLimiter(r, RateLimitOptions{
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
	cfg := apiconfig.NewDefault()
	cfg.Service.RateLimit = nil // No rate limit config
	router := chi.NewRouter()
	InstallRateLimiter(router, RateLimitOptions{
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
	cfg := apiconfig.NewDefault()
	cfg.Service.RateLimit = &common.RateLimitConfig{
		Requests: 100,
		Window:   util.Duration(5 * time.Minute),
	}
	router := chi.NewRouter()
	InstallRateLimiter(router, RateLimitOptions{
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
		cfg := apiconfig.NewDefault()
		cfg.Service.RateLimit = &common.RateLimitConfig{
			Requests:     60,
			Window:       util.Duration(time.Minute),
			AuthRequests: 3,
			AuthWindow:   util.Duration(30 * time.Second),
		}

		router := chi.NewRouter()
		InstallRateLimiter(router, RateLimitOptions{
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
		cfg := apiconfig.NewDefault()
		cfg.Service.RateLimit = &common.RateLimitConfig{
			Requests: 5,
			Window:   util.Duration(30 * time.Second),
			// No auth-specific config
		}

		router := chi.NewRouter()
		InstallRateLimiter(router, RateLimitOptions{
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
	cfg := apiconfig.NewDefault()
	cfg.Service.RateLimit = &common.RateLimitConfig{
		Requests: 3,
		Window:   util.Duration(30 * time.Second),
	}

	router := chi.NewRouter()
	InstallRateLimiter(router, RateLimitOptions{
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
	cfg := apiconfig.NewDefault()
	cfg.Service.RateLimit = &common.RateLimitConfig{
		Requests: 3,
		Window:   util.Duration(30 * time.Second),
	}

	router := chi.NewRouter()
	InstallRateLimiter(router, RateLimitOptions{
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
		cfgNoProxies := apiconfig.NewDefault()
		cfgNoProxies.Service.RateLimit = &common.RateLimitConfig{
			Requests: 3,
			Window:   util.Duration(30 * time.Second),
			// No trusted proxies configured
		}

		routerNoProxies := chi.NewRouter()
		InstallRateLimiter(routerNoProxies, RateLimitOptions{
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
		cfgCIDR := apiconfig.NewDefault()
		cfgCIDR.Service.RateLimit = &common.RateLimitConfig{
			Requests: 3,
			Window:   util.Duration(30 * time.Second),
		}

		routerCIDR := chi.NewRouter()
		InstallRateLimiter(routerCIDR, RateLimitOptions{
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
