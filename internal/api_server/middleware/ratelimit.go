package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
)

// RateLimitOptions configures rate limiting behavior
type RateLimitOptions struct {
	Requests       int
	Window         time.Duration
	Message        string
	TrustedProxies []string
}

// getClientIPFromRequest extracts the client IP from the request's RemoteAddr
// Returns the IP portion, falling back to the full RemoteAddr if parsing fails
func getClientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// IPRateLimiter creates an IP-based rate limiter.
// This is the traditional rate limiter that works by client IP address.
// Note: Should be used with TrustedRealIP middleware for proper proxy handling
func IPRateLimiter(requests int, window time.Duration, message string) func(http.Handler) http.Handler {
	limiter := httprate.Limit(
		requests,
		window,
		httprate.WithKeyFuncs(func(r *http.Request) (string, error) {
			// Use the client IP as the key
			// Note: r.RemoteAddr will be the real IP if TrustedRealIP middleware is used
			return getClientIPFromRequest(r), nil
		}),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    http.StatusTooManyRequests,
				"message": message,
				"reason":  "TooManyRequests",
			}); err != nil {
				// If JSON encoding fails, we can't do much more
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}),
	)

	return limiter
}

// InstallIPRateLimiter installs RealIP + IP-based rate limiter.
// This is a convenience function that applies both TrustedRealIP and IPRateLimiter.
// Note: This function is deprecated. Use TrustedRealIP + IPRateLimiter directly instead.
func InstallIPRateLimiter(r chi.Router, opts RateLimitOptions) {
	// 1) Only trust X-Forwarded-For/X-Real-IP when the immediate peer is in one of these CIDRs
	if len(opts.TrustedProxies) > 0 {
		r.Use(TrustedRealIP(opts.TrustedProxies))
	}
	// 2) Apply IP-based rate limiting
	r.Use(IPRateLimiter(opts.Requests, opts.Window, opts.Message))
}

// TrustedRealIP middleware extracts the real client IP from trusted proxy headers.
// It only trusts X-Forwarded-For, X-Real-IP, and True-Client-IP headers when the
// immediate peer (r.RemoteAddr) is in the trustedProxies list.
// If the peer is not trusted, headers are silently ignored and r.RemoteAddr is used.
func TrustedRealIP(trustedProxies []string) func(http.Handler) http.Handler {
	// Pre-parse trusted proxy CIDRs and literal IPs once at middleware construction time
	// This avoids parsing on every request, keeping the hot path allocation-free
	var trustedNets []*net.IPNet
	for _, entry := range trustedProxies {
		s := strings.TrimSpace(entry)
		if s == "" {
			continue
		}
		if strings.Contains(s, "/") {
			if _, n, err := net.ParseCIDR(s); err == nil {
				trustedNets = append(trustedNets, n)
			}
			continue
		}
		if ip := net.ParseIP(s); ip != nil {
			// Convert literal IP to a single-host network
			if ip.To4() != nil {
				trustedNets = append(trustedNets, &net.IPNet{IP: ip, Mask: net.CIDRMask(32, 32)})
			} else {
				trustedNets = append(trustedNets, &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)})
			}
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if the immediate peer is trusted
			if len(trustedNets) > 0 {
				host := getClientIPFromRequest(r)
				if peerIP := net.ParseIP(host); peerIP != nil {
					for _, trustedNet := range trustedNets {
						if trustedNet.Contains(peerIP) {
							// Peer is trusted, extract real IP from headers
							// Priority: True-Client-IP > X-Real-IP > X-Forwarded-For
							if tc := strings.TrimSpace(r.Header.Get("True-Client-IP")); tc != "" {
								if ip := net.ParseIP(tc); ip != nil {
									r.RemoteAddr = ip.String()
									break
								}
							}
							if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
								if ip := net.ParseIP(xr); ip != nil {
									r.RemoteAddr = ip.String()
									break
								}
							}
							if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
								first := strings.TrimSpace(strings.Split(xff, ",")[0])
								if ip := net.ParseIP(first); ip != nil {
									r.RemoteAddr = ip.String()
									break
								}
							}
							break
						}
					}
				}
				// silent ignore: untrusted headers are simply ignored, no logging or blocking
			}
			next.ServeHTTP(w, r)
		})
	}
}

// UserIdentityRateLimiter creates a rate limiter keyed by user identity (username/UID) from authenticated context
// This is used for authenticated endpoints where we know the user identity
// Note: Should be used after authentication middleware
func UserIdentityRateLimiter(requests int, window time.Duration, message string) func(http.Handler) http.Handler {
	limiter := httprate.Limit(
		requests,
		window,
		httprate.WithKeyFuncs(func(r *http.Request) (string, error) {
			// Extract user identity from context
			ctx := r.Context()
			identity, err := common.GetIdentity(ctx)
			if err != nil {
				// Fallback to IP if no identity available
				return getClientIPFromRequest(r), nil
			}

			// Use username or UID as the key
			if username := identity.GetUsername(); username != "" {
				return fmt.Sprintf("user:%s", username), nil
			}
			if uid := identity.GetUID(); uid != "" {
				return fmt.Sprintf("uid:%s", uid), nil
			}

			// Fallback to IP if no username/UID
			return getClientIPFromRequest(r), nil
		}),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    http.StatusTooManyRequests,
				"message": message,
				"reason":  "TooManyRequests",
			}); err != nil {
				// If JSON encoding fails, we can't do much more
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}),
	)

	return limiter
}

// DeviceIdentityRateLimiter creates a rate limiter keyed by device fingerprint from mTLS certificate
// This is used for agent endpoints where devices authenticate via mTLS
// Note: Should be used after TLS middleware that extracts the peer certificate
func DeviceIdentityRateLimiter(requests int, window time.Duration, message string) func(http.Handler) http.Handler {
	limiter := httprate.Limit(
		requests,
		window,
		httprate.WithKeyFuncs(func(r *http.Request) (string, error) {
			// Extract device fingerprint from TLS peer certificate
			ctx := r.Context()
			peerCertificate, err := signer.PeerCertificateFromCtx(ctx)
			if err != nil {
				// Fallback to IP if no certificate available
				return getClientIPFromRequest(r), nil
			}

			// Extract device fingerprint from certificate
			fingerprint, err := signer.GetDeviceFingerprintExtension(peerCertificate)
			if err != nil {
				// Fallback to IP if no fingerprint available
				return getClientIPFromRequest(r), nil
			}

			// Use device fingerprint as the key
			return fmt.Sprintf("device:%s", fingerprint), nil
		}),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(window.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			if err := json.NewEncoder(w).Encode(map[string]interface{}{
				"code":    http.StatusTooManyRequests,
				"message": message,
				"reason":  "TooManyRequests",
			}); err != nil {
				// If JSON encoding fails, we can't do much more
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			}
		}),
	)

	return limiter
}
