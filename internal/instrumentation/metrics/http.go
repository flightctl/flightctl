package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
)

type ServiceType string

const (
	ServiceTypeAgentAPI ServiceType = "agent-api"
	ServiceTypeUserAPI  ServiceType = "user-api"
	ServiceTypeUnknown  ServiceType = "unknown"
)

type HTTPMetrics interface {
	RecordRequest(statusCode int, duration time.Duration, source string)
	GetMiddleware(serviceType ServiceType) func(http.Handler) http.Handler
	MetricsName() string
	Describe(ch chan<- *prometheus.Desc)
	Collect(ch chan<- prometheus.Metric)
}

type HTTPMetricsCollector struct {
	requestsTotal    *prometheus.CounterVec
	requestLatencies *prometheus.HistogramVec
	errorsTotal      *prometheus.CounterVec
	config           *config.Config
}

type NullHTTPMetrics struct{}

func (n *NullHTTPMetrics) RecordRequest(statusCode int, duration time.Duration, source string) {}
func (n *NullHTTPMetrics) GetMiddleware(serviceType ServiceType) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return next
	}
}
func (n *NullHTTPMetrics) MetricsName() string                 { return "null-http-metrics" }
func (n *NullHTTPMetrics) Describe(ch chan<- *prometheus.Desc) {}
func (n *NullHTTPMetrics) Collect(ch chan<- prometheus.Metric) {}

// NewHTTPMetrics creates a new HTTP metrics collector or returns a null implementation
func NewHTTPMetrics(cfg *config.Config) HTTPMetrics {
	if cfg == nil {
		return &NullHTTPMetrics{}
	}

	return &HTTPMetricsCollector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_requests_total",
				Help: "Total number of HTTP API requests",
			},
			[]string{"source"},
		),
		requestLatencies: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "flightctl_api_latencies_seconds",
				Help:    "Duration of HTTP API requests (binned in buckets)",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"status", "code", "source"},
		),
		errorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_errors_total",
				Help: "Count of failed HTTP requests (4xx, 5xx)",
			},
			[]string{"code", "source"},
		),
		config: cfg,
	}
}

func NewHTTPMetricsCollector(cfg *config.Config) *HTTPMetricsCollector {
	metrics := NewHTTPMetrics(cfg)
	if collector, ok := metrics.(*HTTPMetricsCollector); ok {
		return collector
	}

	return &HTTPMetricsCollector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_requests_total",
				Help: "Total number of HTTP API requests",
			},
			[]string{"source"},
		),
		requestLatencies: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "flightctl_api_latencies_seconds",
				Help:    "Duration of HTTP API requests (binned in buckets)",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"status", "code", "source"},
		),
		errorsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "flightctl_api_errors_total",
				Help: "Count of failed HTTP requests (4xx, 5xx)",
			},
			[]string{"code", "source"},
		),
		config: cfg,
	}
}

func (c *HTTPMetricsCollector) MetricsName() string {
	return "http"
}

func (c *HTTPMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	c.requestsTotal.Describe(ch)
	c.requestLatencies.Describe(ch)
	c.errorsTotal.Describe(ch)
}

func (c *HTTPMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	c.requestsTotal.Collect(ch)
	c.requestLatencies.Collect(ch)
	c.errorsTotal.Collect(ch)
}

// RecordRequest records metrics for an HTTP request.
func (c *HTTPMetricsCollector) RecordRequest(statusCode int, duration time.Duration, source string) {
	statusStr := strconv.Itoa(statusCode)

	// Record total requests
	c.requestsTotal.WithLabelValues(source).Inc()

	// Record latency
	statusCategory := getStatusCategory(statusCode)
	c.requestLatencies.WithLabelValues(statusCategory, statusStr, source).Observe(duration.Seconds())

	// Record errors for 4xx and 5xx status codes
	if statusCode >= 400 {
		c.errorsTotal.WithLabelValues(statusStr, source).Inc()
	}
}

// GetMiddleware returns HTTP middleware that conditionally wraps with OpenTelemetry and/or custom metrics
// serviceType should be "agent-api" or "user-api" to differentiate traffic types in OpenTelemetry metrics
func (c *HTTPMetricsCollector) GetMiddleware(serviceType ServiceType) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		// Validate serviceType parameter
		if serviceType == "" {
			serviceType = ServiceTypeUnknown
		}

		var handler http.Handler = next

		// Check if custom HTTP metrics are enabled
		if c.config != nil && c.config.Prometheus != nil && c.config.Prometheus.CustomHTTPEnabled {
			// Wrap with custom metrics collection
			handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				start := time.Now()

				// Create response writer wrapper to capture status code
				ww := &responseWriterWrapper{
					ResponseWriter: w,
					statusCode:     200,
				}

				// Call the next handler (NOT handler - that would be recursive!)
				next.ServeHTTP(ww, r)

				// Record custom metrics using serviceType as source
				duration := time.Since(start)
				c.RecordRequest(ww.statusCode, duration, string(serviceType))
			})
		} else {
			// If custom metrics disabled, just use the original handler
			handler = next
		}

		// Check if OpenTelemetry metrics are enabled
		if c.config != nil && c.config.HTTPOtelMetrics != nil && c.config.HTTPOtelMetrics.Enabled {
			// Configure service name based on service type and configuration
			serviceName := string(serviceType)
			if c.config.HTTPOtelMetrics.ServiceName != "" {
				// Use configured service name as prefix with service type
				serviceName = c.config.HTTPOtelMetrics.ServiceName + "-" + string(serviceType)
			}

			// Create OpenTelemetry options
			opts := []otelhttp.Option{
				otelhttp.WithMeterProvider(otel.GetMeterProvider()),
			}

			// Wrap with OpenTelemetry for standard metrics
			handler = otelhttp.NewHandler(handler, serviceName, opts...)
		}

		return handler
	}
}

// responseWriterWrapper wraps http.ResponseWriter to capture status code.
type responseWriterWrapper struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriterWrapper) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *responseWriterWrapper) Write(b []byte) (int, error) {
	return rw.ResponseWriter.Write(b)
}

func getStatusCategory(statusCode int) string {
	switch {
	case statusCode >= 200 && statusCode < 400:
		return "success"
	case statusCode >= 400:
		return "failed"
	default:
		return "failed"
	}
}
