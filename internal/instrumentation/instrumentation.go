package instrumentation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/mackerelio/go-osstat/cpu"
	"github.com/mackerelio/go-osstat/memory"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	readTimeout             = 5 * time.Second
	writeTimeout            = 10 * time.Second
)

type MetricsServer struct {
	log      logrus.FieldLogger
	cfg      *config.Config
	registry *prometheus.Registry
	metrics  *ApiMetrics
}

type ApiMetrics struct {
	SloMax float64

	SuccessLatency prometheus.Histogram
	ErrorLatency   prometheus.Histogram

	ApiTraffic   prometheus.Counter
	AgentTraffic prometheus.Counter

	SloViolations prometheus.Counter
	ClientErrors  prometheus.Counter
	ServerErrors  prometheus.Counter

	CpuUtilization    prometheus.Gauge
	MemoryUtilization prometheus.Gauge
	DiskUtilization   prometheus.Gauge
}

func NewApiMetrics(cfg *config.Config) *ApiMetrics {
	return &ApiMetrics{
		SloMax: cfg.Prometheus.SloMax,
		ApiTraffic: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_requests_api_total",
			Help: "Number of requests to Flightctl API server",
		}),
		AgentTraffic: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_requests_agent_total",
			Help: "Number of requests to Flightctl Agent server",
		}),
		SuccessLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_api_latencies_success_seconds",
			Help:    "Distribution of latencies of Flightctl server responses that encountered no errors",
			Buckets: cfg.Prometheus.ApiLatencyBins,
		}),
		ErrorLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "flightctl_api_latencies_error_seconds",
			Help:    "Distribution of latencies of Flightctl server responses that encountered errors",
			Buckets: cfg.Prometheus.ApiLatencyBins,
		}),
		SloViolations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_slo_total",
			Help: "Number of Flightctl server responses that exceeded SLO",
		}),
		ClientErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_client_total",
			Help: "Number of Flightctl server responses that encountered client (400) errors",
		}),
		ServerErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "flightctl_api_errors_server_total",
			Help: "Number of Flightctl server responses that encountered server (500) errors",
		}),
		CpuUtilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_api_cpu_utilization",
			Help: "Flightctl server CPU utilization",
		}),
		MemoryUtilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_api_memory_utilization",
			Help: "Flightctl server memory utilization",
		}),
		DiskUtilization: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "flightctl_api_disk_utilization",
			Help: "Flightctl server storage utilization",
		}),
	}
}

func NewMetricsServer(
	log logrus.FieldLogger,
	cfg *config.Config,
	metrics *ApiMetrics,
) *MetricsServer {
	return &MetricsServer{
		log:      log,
		cfg:      cfg,
		metrics:  metrics,
		registry: prometheus.NewRegistry(),
	}
}

// We need to access the HTTP status code in our instrumentation middleware
// ResponseWriter does not let us do this, so wrap it in an
// interface that will catch and save the written status code
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func NewLoggingResponseWriter(w http.ResponseWriter) *loggingResponseWriter {
	return &loggingResponseWriter{
		w,
		0,
	}
}

func (lw *loggingResponseWriter) WriteHeader(statusCode int) {
	lw.statusCode = statusCode
	lw.ResponseWriter.WriteHeader(statusCode)
}

func (m *MetricsServer) Run(ctx context.Context) error {
	m.metrics.RegisterWith(m.registry)

	srv := &http.Server{
		Addr:         m.cfg.Prometheus.Address,
		Handler:      promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry}),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
	}

	go m.auditCpuWorker(ctx)
	go m.auditMemoryWorker(ctx)
	go m.auditDiskWorker(ctx)

	go func() {
		<-ctx.Done()
		m.log.Println("Shutdown signal received:", ctx.Err())
		ctxTimeout, cancel := context.WithTimeout(context.Background(), gracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		_ = srv.Shutdown(ctxTimeout)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}

	return nil
}

func (m *MetricsServer) auditCpuWorker(ctx context.Context) {
	var lastIdle uint64 = 0
	var lastTotal uint64 = 0

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Stopping CPU audit")
			return
		case <-ticker.C:
			stats, err := cpu.Get()
			if err != nil {
				m.log.Errorf("Could not audit cpu usage: %v", err)
			}

			// stats from /proc/stat increase monotonically, so we must
			// compute the delta from our last audit
			m.metrics.CpuUtilization.Set(
				1.0 - float64(stats.Idle-lastIdle)/float64(stats.Total-lastTotal),
			)
			lastIdle = stats.Idle
			lastTotal = stats.Total
		}
	}
}

func (m *MetricsServer) auditMemoryWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Stopping memory audit")
			return
		case <-ticker.C:
			stats, err := memory.Get()
			if err != nil {
				m.log.Errorf("could not audit memory usage: %v", err)
			}

			m.metrics.MemoryUtilization.Set(
				float64(stats.Used) / float64(stats.Total),
			)
		}
	}
}

func (m *MetricsServer) auditDiskWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second * 5)
	defer ticker.Stop()

	var stat unix.Statfs_t

	for {
		select {
		case <-ctx.Done():
			m.log.Debug("Stopping disk audit")
			return
		case <-ticker.C:
			err := unix.Statfs("/", &stat)
			if err != nil {
				fmt.Println("could not audit disk usage: ", err)
			}

			m.metrics.DiskUtilization.Set(
				1.0 - float64(stat.Bfree)/float64(stat.Blocks),
			)
		}
	}
}

func (m *ApiMetrics) RegisterWith(reg *prometheus.Registry) {
	reg.MustRegister(m.SuccessLatency)
	reg.MustRegister(m.ErrorLatency)
	reg.MustRegister(m.ApiTraffic)
	reg.MustRegister(m.AgentTraffic)
	reg.MustRegister(m.SloViolations)
	reg.MustRegister(m.ServerErrors)
	reg.MustRegister(m.CpuUtilization)
	reg.MustRegister(m.MemoryUtilization)
	reg.MustRegister(m.DiskUtilization)

}

func (m *ApiMetrics) AgentServerMiddleware(next http.Handler) http.Handler {
	return m.ServerMiddleware(next, true)
}

func (m *ApiMetrics) ApiServerMiddleware(next http.Handler) http.Handler {
	return m.ServerMiddleware(next, false)
}

func (m *ApiMetrics) ServerMiddleware(next http.Handler, agentServer bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if agentServer {
			m.AgentTraffic.Inc()
		} else {
			m.ApiTraffic.Inc()
		}

		lw := NewLoggingResponseWriter(w)
		next.ServeHTTP(lw, r)
		statusClass := lw.statusCode - lw.statusCode%100

		if statusClass == 400 {
			m.ClientErrors.Inc()
		}

		if statusClass == 500 {
			m.ServerErrors.Inc()
		}

		thisLatency := time.Since(start).Seconds()

		if statusClass == 200 && thisLatency > m.SloMax {
			m.SloViolations.Inc()
		}

		if statusClass == 200 {
			m.SuccessLatency.Observe(thisLatency)
		} else {
			m.ErrorLatency.Observe(thisLatency)
		}
	})
}
