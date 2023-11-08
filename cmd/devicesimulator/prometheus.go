package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog/v2"
)

var (
	metricNamespace                  = "flightctl"
	metricSubsystem                  = "devicesimulator"
	metricLabelResultError           = "error"
	metricLabelResultOk              = "ok"
	metricLabelOperationGetSpec      = "get_spec"
	metricLabelOperationUpdateStatus = "update_status"

	rpcCalls = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "rpc_calls_total",
			Help:      "The total number of rpc calls (successful and failed) partitioned by operation",
		},
		[]string{"operation"},
	)
	rpcDurations = prometheus.NewSummaryVec(
		prometheus.SummaryOpts{
			Namespace:  metricNamespace,
			Subsystem:  metricSubsystem,
			Name:       "rpc_durations_seconds",
			Help:       "Latency distributions of RPC calls partitioned by operation and result",
			Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001},
		},
		[]string{"operation", "result"},
	)
	rpcErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: metricNamespace,
			Subsystem: metricSubsystem,
			Name:      "rpc_errors_total",
			Help:      "The total number of rpc calls that failed partitioned by operation and error type",
		},
		[]string{"operation", "error"},
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
	prometheus.MustRegister(rpcDurations)
	prometheus.MustRegister(rpcErrors)
	prometheus.MustRegister(rpcCalls)
}

func rpcMetricsCallback(operation string, duractionSeconds float64, err error) {
	rpcCalls.WithLabelValues(operation).Inc()
	if err != nil {
		errorType := reasonFromAPIError(err)
		rpcErrors.WithLabelValues(operation, errorType).Inc()
		rpcDurations.WithLabelValues(operation, metricLabelResultError).Observe(duractionSeconds)
	} else {
		rpcDurations.WithLabelValues(operation, metricLabelResultOk).Observe(duractionSeconds)
	}
}

func reasonFromAPIError(err error) string {
	errorType := "unknown"
	return errorType
}
