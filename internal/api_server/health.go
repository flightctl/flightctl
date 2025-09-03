package apiserver

import (
	"context"
	"net/http"
	"time"
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
