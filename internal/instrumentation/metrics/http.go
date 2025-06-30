package metrics

import (
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// HTTPCollector implements NamedCollector and gathers HTTP request metrics
type HTTPCollector struct {
	sloMax float64

	requestsTotal *prometheus.CounterVec
	latencies     *prometheus.HistogramVec
	errorsTotal   *prometheus.CounterVec
}

// NewHTTPCollector creates a new HTTP metrics collector with the provided configuration
func NewHTTPCollector(cfg *config.Config) *HTTPCollector {
	if cfg.Prometheus == nil {
		return nil
	}

	return &HTTPCollector{
		sloMax: cfg.Prometheus.SloMax,
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_requests_total",
				Help: "Total number of HTTP API requests",
			},
			[]string{"source"},
		),
		latencies: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "flightctl_api_latencies_seconds",
				Help:    "Duration of HTTP API requests (binned in buckets)",
				Buckets: cfg.Prometheus.ApiLatencyBins,
			},
			[]string{"status", "code", "source"},
		),
		errorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_errors_total",
				Help: "Count of failed HTTP requests (4xx, 5xx)",
			},
			[]string{"code", "bucket", "source"},
		),
	}
}

func (h *HTTPCollector) MetricsName() string {
	return "http"
}

func (h *HTTPCollector) Describe(ch chan<- *prometheus.Desc) {
	h.requestsTotal.Describe(ch)
	h.latencies.Describe(ch)
	h.errorsTotal.Describe(ch)
}

func (h *HTTPCollector) Collect(ch chan<- prometheus.Metric) {
	h.requestsTotal.Collect(ch)
	h.latencies.Collect(ch)
	h.errorsTotal.Collect(ch)
}

// loggingResponseWriter wraps http.ResponseWriter to capture the status code
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func newLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{
		ResponseWriter: w,
		statusCode:     0,
	}
}

func (lw *loggingResponseWriter) WriteHeader(statusCode int) {
	lw.statusCode = statusCode
	lw.ResponseWriter.WriteHeader(statusCode)
}

// AgentServerMiddleware returns middleware for instrumenting agent server requests
func (h *HTTPCollector) AgentServerMiddleware(next http.Handler) http.Handler {
	return h.serverMiddleware(next, true)
}

// APIServerMiddleware returns middleware for instrumenting API server requests
func (h *HTTPCollector) APIServerMiddleware(next http.Handler) http.Handler {
	return h.serverMiddleware(next, false)
}

// serverMiddleware provides HTTP request instrumentation
func (h *HTTPCollector) serverMiddleware(next http.Handler, agentServer bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Determine source label
		source := "api"
		if agentServer {
			source = "agent"
		}

		// Increment total requests counter
		h.requestsTotal.WithLabelValues(source).Inc()

		lw := newLoggingResponseWriter(w)
		next.ServeHTTP(lw, r)

		statusCode := lw.statusCode
		if statusCode == 0 {
			statusCode = 200 // Default to 200 if no status was set
		}

		statusClass := statusCode - statusCode%100
		thisLatency := time.Since(start).Seconds()

		// Record latency with labels
		status := "success"
		if statusClass >= 400 {
			status = "error"
		}
		h.latencies.WithLabelValues(status, fmt.Sprintf("%d", statusCode), source).Observe(thisLatency)

		// Record errors with labels
		if statusClass >= 400 {
			bucket := fmt.Sprintf("%dxx", statusClass/100)
			h.errorsTotal.WithLabelValues(fmt.Sprintf("%d", statusCode), bucket, source).Inc()
		}
	})
}
