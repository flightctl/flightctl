package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

var (
	metricNamespace        = "flightctl"
	metricSubsystem        = "devicesimulator"
	metricLabelResultError = "error"
	metricLabelResultOk    = "ok"

	activeAgents = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "active_agent_count",
			Help:      "Current number of active agents",
		},
	)
	apiRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "api_requests_total",
			Help:      "Total number of API calls, partitioned by operation",
		},
		[]string{"operation"},
	)
	apiErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "api_errors_total",
			Help:      "Total number of API calls returning an error, partitioned by operation and type of error",
		},
		[]string{"operation", "error"},
	)
	apiRequestDurations = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "api_request_duration_seconds",
			Help:      "The response time of API calls, partitioned by operation and result",
			Buckets:   []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1.0},
		},
		[]string{"operation", "result"},
	)
)

func setupMetricsEndpoint(metricsAddress string) {
	http.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: metricsAddress}
	go func() {
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			klog.Errorf("metric server listen on %s: %v", metricsAddress, err)
		}
	}()
	prometheus.MustRegister(activeAgents)
	prometheus.MustRegister(apiRequests)
	prometheus.MustRegister(apiErrors)
	prometheus.MustRegister(apiRequestDurations)
}

func rpcMetricsCallback(operation string, duractionSeconds float64, err error) {
	apiRequests.WithLabelValues(operation).Inc()
	if err != nil {
		errorType := reasonFromAPIError(err)
		apiErrors.WithLabelValues(operation, errorType).Inc()
		apiRequestDurations.WithLabelValues(operation, metricLabelResultError).Observe(duractionSeconds)
	} else {
		apiRequestDurations.WithLabelValues(operation, metricLabelResultOk).Observe(duractionSeconds)
	}
}

func reasonFromAPIError(err error) string {
	errorType := "unknown"
	return errorType
}
