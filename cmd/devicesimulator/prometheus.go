package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/apimachinery/pkg/util/wait"
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
	enrollmentOutcomes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "enrollment_outcomes_total",
			Help:      "Total enrollment wait/approve outcomes, partitioned by result",
		},
		[]string{"result"},
	)
)

func setupMetricsEndpoint(metricsAddress string) {
	http.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: metricsAddress, ReadHeaderTimeout: time.Second}
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
	prometheus.MustRegister(enrollmentOutcomes)
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
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		msg := opErr.Error()
		if strings.Contains(msg, "cannot assign requested address") {
			return "bind"
		}
		if opErr.Op == "dial" {
			return "dial"
		}
		return "connection"
	}
	msg := err.Error()
	if strings.Contains(msg, "cannot assign requested address") {
		return "bind"
	}
	if strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") {
		return "connection"
	}
	if strings.Contains(msg, "dial ") {
		return "dial"
	}
	return "unknown"
}

func recordEnrollmentOutcome(ctx context.Context, err error) {
	switch {
	case err == nil:
		enrollmentOutcomes.WithLabelValues("approved").Inc()
	case ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled):
		enrollmentOutcomes.WithLabelValues("cancelled").Inc()
	case wait.Interrupted(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		enrollmentOutcomes.WithLabelValues("timeout").Inc()
	default:
		enrollmentOutcomes.WithLabelValues("error").Inc()
	}
}
