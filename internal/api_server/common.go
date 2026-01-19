package apiserver

import (
	"fmt"
	"net/http"
	"time"

	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/go-chi/chi/v5"
)

// GracefulShutdownTimeout is the duration to wait for graceful shutdown
const GracefulShutdownTimeout = 5 * time.Second

// OapiErrorHandler is a shared error handler for OpenAPI validation errors
func OapiErrorHandler(w http.ResponseWriter, message string, statusCode int) {
	http.Error(w, fmt.Sprintf("API Error: %s", message), statusCode)
}

// RateLimitConfig holds rate limiting parameters
type RateLimitConfig struct {
	Requests       int
	Window         time.Duration
	TrustedProxies []string
	Message        string
}

type RateLimitDefaults struct {
	Requests int
	Window   time.Duration
}

type RateLimitScope int

const (
	RateLimitScopeGeneral RateLimitScope = iota
	RateLimitScopeAuth
)

type RateLimitScopeDefaults struct {
	Defaults RateLimitDefaults
	Message  string
}

var rateLimitScopeDefaults = map[RateLimitScope]RateLimitScopeDefaults{
	RateLimitScopeGeneral: {
		Defaults: RateLimitDefaults{Requests: 300, Window: time.Minute},
		Message:  "Rate limit exceeded, please try again later",
	},
	RateLimitScopeAuth: {
		Defaults: RateLimitDefaults{Requests: 20, Window: time.Hour},
		Message:  "Login rate limit exceeded, please try again later",
	},
}

type rateLimitOptions struct {
	defaults          *RateLimitDefaults
	message           *string
	trustedProxies    []string
	trustedProxiesSet bool
}

type RateLimitOption func(*rateLimitOptions)

func WithMessage(message string) RateLimitOption {
	return func(opts *rateLimitOptions) {
		opts.message = &message
	}
}

func WithRate(defaults RateLimitDefaults) RateLimitOption {
	return func(opts *rateLimitOptions) {
		opts.defaults = &defaults
	}
}

func WithTrustedProxies(trustedProxies []string) RateLimitOption {
	return func(opts *rateLimitOptions) {
		opts.trustedProxies = trustedProxies
		opts.trustedProxiesSet = true
	}
}

func WithNoTrustedProxies() RateLimitOption {
	return func(opts *rateLimitOptions) {
		opts.trustedProxies = []string{}
		opts.trustedProxiesSet = true
	}
}

// ConfigureRateLimiter adds rate limiting to a router with the given config
func ConfigureRateLimiter(r chi.Router, cfg RateLimitConfig) {
	fcmiddleware.InstallRateLimiter(r, fcmiddleware.RateLimitOptions{
		Requests:       cfg.Requests,
		Window:         cfg.Window,
		Message:        cfg.Message,
		TrustedProxies: cfg.TrustedProxies,
	})
}

// ConfigureRateLimiterFromConfig applies rate limiting based on service config.
// If trustedProxies is unset, it falls back to cfg.TrustedProxies.
func ConfigureRateLimiterFromConfig(
	r chi.Router,
	cfg *config.RateLimitConfig,
	scope RateLimitScope,
	options ...RateLimitOption,
) {
	scopeDefaults, ok := rateLimitScopeDefaults[scope]
	if !ok {
		return
	}

	opts := rateLimitOptions{
		defaults: &scopeDefaults.Defaults,
		message:  &scopeDefaults.Message,
	}
	for _, opt := range options {
		opt(&opts)
	}

	defaults := *opts.defaults
	message := *opts.message
	var trustedProxies []string
	if opts.trustedProxiesSet {
		trustedProxies = opts.trustedProxies
	}

	rateCfg, ok := rateLimitConfigFromConfig(cfg, defaults, message, trustedProxies, scope)
	if !ok {
		return
	}
	ConfigureRateLimiter(r, *rateCfg)
}

func rateLimitConfigFromConfig(
	cfg *config.RateLimitConfig,
	defaults RateLimitDefaults,
	message string,
	trustedProxies []string,
	scope RateLimitScope,
) (*RateLimitConfig, bool) {
	if cfg == nil || !cfg.Enabled {
		return nil, false
	}

	requests := defaults.Requests
	window := defaults.Window
	switch scope {
	case RateLimitScopeAuth:
		if cfg.AuthRequests > 0 {
			requests = cfg.AuthRequests
		}
		if cfg.AuthWindow > 0 {
			window = time.Duration(cfg.AuthWindow)
		}
	case RateLimitScopeGeneral:
		if cfg.Requests > 0 {
			requests = cfg.Requests
		}
		if cfg.Window > 0 {
			window = time.Duration(cfg.Window)
		}
	default:
		return nil, false
	}

	if trustedProxies == nil {
		trustedProxies = cfg.TrustedProxies
	}

	return &RateLimitConfig{
		Requests:       requests,
		Window:         window,
		Message:        message,
		TrustedProxies: trustedProxies,
	}, true
}
