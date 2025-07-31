package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
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

// InstallRateLimiter installs RealIP + custom rate limiter.
func InstallRateLimiter(r chi.Router, opts RateLimitOptions) {
	// 1) Only trust X-Forwarded-For/X-Real-IP when the immediate peer is in one of these CIDRs
	if len(opts.TrustedProxies) > 0 {
		r.Use(TrustedRealIP(opts.TrustedProxies))
	}

	// 2) Build a limiter: N req per window, keyed by client IP
	limiter := httprate.Limit(
		opts.Requests, // e.g. 60
		opts.Window,   // e.g. time.Minute
		httprate.WithKeyFuncs( // bucket by r.RemoteAddr (after RealIP)
			httprate.KeyByIP,
		),
		httprate.WithLimitHandler(func(w http.ResponseWriter, r *http.Request) {
			// build your API status
			status := api.Status{
				Code:    http.StatusTooManyRequests,
				Message: opts.Message,
				Reason:  "TooManyRequests",
			}

			// emit headers + JSON
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", strconv.Itoa(int(opts.Window.Seconds())))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(status)
		}),
	)

	// 3) Register it for all routes (user + agent routers)
	r.Use(limiter)
}

// TrustedRealIP only rewrites RemoteAddr when the immediate peer is in one of your LB CIDRs
func TrustedRealIP(trustedCIDRs []string) func(http.Handler) http.Handler {
	// parse CIDRs once
	var nets []*net.IPNet
	for _, cidr := range trustedCIDRs {
		if _, n, err := net.ParseCIDR(cidr); err == nil {
			nets = append(nets, n)
		}
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// extract peer IP
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err == nil {
				peerIP := net.ParseIP(host)

				// check if peer is trusted
				isTrusted := false
				for _, n := range nets {
					if n.Contains(peerIP) {
						isTrusted = true
						break
					}
				}

				if isTrusted {
					// only trust and process headers from trusted peers
					// Follow chi middleware order: True-Client-IP -> X-Real-IP -> X-Forwarded-For
					if tci := r.Header.Get("True-Client-IP"); tci != "" {
						r.RemoteAddr = tci
					} else if xr := r.Header.Get("X-Real-IP"); xr != "" {
						r.RemoteAddr = xr
					} else if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
						r.RemoteAddr = strings.TrimSpace(strings.Split(xff, ",")[0])
					}
				}
				// silent ignore: untrusted headers are simply ignored, no logging or blocking
			}
			next.ServeHTTP(w, r)
		})
	}
}
