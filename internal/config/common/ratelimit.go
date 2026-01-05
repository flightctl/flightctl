package common

import "github.com/flightctl/flightctl/internal/util"

// RateLimitConfig holds rate limiting configuration.
type RateLimitConfig struct {
	Enabled      bool          `json:"enabled,omitempty"`      // Enable/disable rate limiting
	Requests     int           `json:"requests,omitempty"`     // max requests per window
	Window       util.Duration `json:"window,omitempty"`       // e.g. "1m" for one minute
	AuthRequests int           `json:"authRequests,omitempty"` // max auth requests per window
	AuthWindow   util.Duration `json:"authWindow,omitempty"`   // e.g. "1h" for one hour
	// TrustedProxies specifies IP addresses/networks that are allowed to set proxy headers
	// If empty, proxy headers are ignored for security (only direct connection IPs are used)
	TrustedProxies []string `json:"trustedProxies,omitempty"`
}
