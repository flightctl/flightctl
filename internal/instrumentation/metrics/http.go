package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// HTTPCollector implements NamedCollector and gathers HTTP request metrics
// including latencies, traffic counts, error rates, and SLO violations.
type HTTPCollector struct {
	// Latency metrics
	successLatency prometheus.Histogram
	errorLatency   prometheus.Histogram

	// Traffic counters
	apiTraffic   prometheus.Counter
	agentTraffic prometheus.Counter

	// Error counters
	sloViolations prometheus.Counter
	clientErrors  prometheus.Counter
	serverErrors  prometheus.Counter

	// Configuration
	sloMax float64
}

// NewHTTPCollector creates a new HTTP metrics collector with configurable SLO threshold.
func NewHTTPCollector(sloMax float64, latencyBins []float64) *HTTPCollector {
	collector := &HTTPCollector{
		sloMax: sloMax,
		successLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_api_latencies_success_seconds",
			Help:    "Distribution of latencies of Flightctl server responses that encountered no errors",
			Buckets: latencyBins,
		}),
		errorLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_api_latencies_error_seconds",
			Help:    "Distribution of latencies of Flightctl server responses that encountered errors",
			Buckets: latencyBins,
		}),
		apiTraffic: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_requests_api_total",
			Help: "Number of requests to Flightctl API server",
		}),
		agentTraffic: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_requests_agent_total",
			Help: "Number of requests to Flightctl Agent server",
		}),
		sloViolations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_slo_total",
			Help: "Number of Flightctl server responses that exceeded SLO",
		}),
		clientErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_client_total",
			Help: "Number of Flightctl server responses that encountered client (400) errors",
		}),
		serverErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_server_total",
			Help: "Number of Flightctl server responses that encountered server (500) errors",
		}),
	}

	return collector
}

func (c *HTTPCollector) MetricsName() string {
	return "http"
}

func (c *HTTPCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.successLatency.Desc()
	ch <- c.errorLatency.Desc()
	ch <- c.apiTraffic.Desc()
	ch <- c.agentTraffic.Desc()
	ch <- c.sloViolations.Desc()
	ch <- c.clientErrors.Desc()
	ch <- c.serverErrors.Desc()
}

func (c *HTTPCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- c.successLatency
	ch <- c.errorLatency
	ch <- c.apiTraffic
	ch <- c.agentTraffic
	ch <- c.sloViolations
	ch <- c.clientErrors
	ch <- c.serverErrors
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

// AgentServerMiddleware returns HTTP middleware for the agent server
func (c *HTTPCollector) AgentServerMiddleware(next http.Handler) http.Handler {
	return c.serverMiddleware(next, true)
}

// ApiServerMiddleware returns HTTP middleware for the API server
func (c *HTTPCollector) ApiServerMiddleware(next http.Handler) http.Handler {
	return c.serverMiddleware(next, false)
}

// serverMiddleware is the core HTTP metrics collection middleware
func (c *HTTPCollector) serverMiddleware(next http.Handler, agentServer bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if agentServer {
			c.agentTraffic.Inc()
		} else {
			c.apiTraffic.Inc()
		}

		lw := newLoggingResponseWriter(w)
		next.ServeHTTP(lw, r)

		statusClass := lw.statusCode - lw.statusCode%100
		thisLatency := time.Since(start).Seconds()

		if statusClass == 400 {
			c.clientErrors.Inc()
		}
		if statusClass == 500 {
			c.serverErrors.Inc()
		}

		if statusClass == 200 && thisLatency > c.sloMax {
			c.sloViolations.Inc()
		}

		if statusClass == 200 {
			c.successLatency.Observe(thisLatency)
		} else {
			c.errorLatency.Observe(thisLatency)
		}
	})
}
