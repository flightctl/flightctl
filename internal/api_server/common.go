package apiserver

import (
	"fmt"
	"net/http"
	"time"

	fcmiddleware "github.com/flightctl/flightctl/internal/api_server/middleware"
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

// ConfigureRateLimiter adds rate limiting to a router with the given config
func ConfigureRateLimiter(r chi.Router, cfg RateLimitConfig) {
	fcmiddleware.InstallRateLimiter(r, fcmiddleware.RateLimitOptions{
		Requests:       cfg.Requests,
		Window:         cfg.Window,
		Message:        cfg.Message,
		TrustedProxies: cfg.TrustedProxies,
	})
}
