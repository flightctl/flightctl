package apiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/pkg/shutdown"
)

// HealthChecker is a minimal contract for readiness checks.
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// ReadyzHandler returns a simple HTTP handler that runs health checks.
// It iterates through provided checks and returns 503 on any failure.
// The response body is empty.
func ReadyzHandler(timeout time.Duration, checks ...HealthChecker) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		to := timeout
		if to <= 0 {
			to = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(r.Context(), to)
		defer cancel()

		for _, c := range checks {
			if c == nil {
				continue
			}
			if err := c.CheckHealth(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
	})
}

// HealthzHandler returns a simple HTTP handler that always returns OK.
// This is for liveness probes that just need to know if the process is running.
// The response body is empty.
func HealthzHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// ShutdownStatusProvider defines the interface for providing shutdown status information
type ShutdownStatusProvider interface {
	GetShutdownStatus() shutdown.ShutdownStatus
}

// ShutdownStatusHandler returns an HTTP handler that provides detailed shutdown status.
// Returns 503 Service Unavailable when shutting down, 200 OK when operational.
// Always includes a JSON response body with detailed status information.
func ShutdownStatusHandler(provider ShutdownStatusProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := provider.GetShutdownStatus()

		w.Header().Set("Content-Type", "application/json")

		// Return 503 if shutting down, 200 if operational
		if status.IsShuttingDown {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		// Always return JSON status information
		if err := json.NewEncoder(w).Encode(status); err != nil {
			// If we can't encode the status, at least return an error response
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"failed to encode shutdown status"}`)) // Ignore write error in error path
		}
	})
}
