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
)

type MetricsServer struct {
	log logrus.FieldLogger
	cfg *config.Config
}

func NewMetricsServer(
	log logrus.FieldLogger,
	cfg *config.Config,
) *MetricsServer {
	return &MetricsServer{
		log: log,
		cfg: cfg,
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

var (
	promRegistry *prometheus.Registry

	success_latency prometheus.Histogram
	error_latency   prometheus.Histogram

	api_traffic   prometheus.Counter
	agent_traffic prometheus.Counter

	slo_violations prometheus.Counter
	client_errors  prometheus.Counter
	server_errors  prometheus.Counter

	cpu_utilization    prometheus.Gauge
	memory_utilization prometheus.Gauge
	disk_utilization   prometheus.Gauge

	slo_max float64 // responses with > this latency (in seconds) are SLO violations
)

func (m *MetricsServer) Run(ctx context.Context) error {
	promRegistry = prometheus.NewRegistry()

	// Total traffic. Distinguish between Agent Server and User API Server
	api_traffic = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "requests_api_total",
	})
	promRegistry.MustRegister(api_traffic)

	agent_traffic = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "requests_agent_total",
	})
	promRegistry.MustRegister(agent_traffic)

	// Latencies. Distinguish between sucess and failure
	success_latency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "latencies_success_seconds",
		Buckets: []float64{1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1e-0},
	})
	promRegistry.MustRegister(success_latency)

	error_latency = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "latencies_error_seconds",
		Buckets: []float64{1e-5, 1e-4, 1e-3, 1e-2, 1e-1, 1e-0},
	})
	promRegistry.MustRegister(error_latency)

	// Errors. Count SLO violations, client (400), and server (500)
	slo_violations = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "errors_slo_total",
	})
	promRegistry.MustRegister(slo_violations)

	client_errors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "errors_client_total",
	})
	promRegistry.MustRegister(client_errors)

	server_errors = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "errors_server_total",
	})
	promRegistry.MustRegister(server_errors)

	// Resource utilization. Audit % utilization of system CPU, memory, and disk
	cpu_utilization = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "utilization_cpu",
	})
	promRegistry.MustRegister(cpu_utilization)
	go m.auditCpuWorker()

	memory_utilization = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "utilization_memory",
	})
	promRegistry.MustRegister(memory_utilization)
	go m.auditMemoryWorker()

	disk_utilization = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "utilization_disk",
	})
	promRegistry.MustRegister(disk_utilization)
	go m.auditDiskWorker()

	slo_max = m.cfg.Prometheus.SLOMax

	srv := &http.Server{
		Addr:         m.cfg.Prometheus.Address,
		Handler:      promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{Registry: promRegistry}),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

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

func (m *MetricsServer) auditCpuWorker() {
	var lastIdle uint64 = 0
	var lastTotal uint64 = 0
	for {
		time.Sleep(time.Second * 5) // Prometheus scrapes every 5s rn
		stats, err := cpu.Get()
		if err != nil {
			m.log.Errorf("Could not audit cpu usage: %v", err)
		}

		// stats from /proc/stat increase monotonically, so we must
		// compute the delta from our last audit
		cpu_utilization.Set(
			float64(stats.Idle-lastIdle) / float64(stats.Total-lastTotal),
		)
		lastIdle = stats.Idle
		lastTotal = stats.Total
	}
}

func (m *MetricsServer) auditMemoryWorker() {
	for {
		time.Sleep(time.Second * 5)

		stats, err := memory.Get()
		if err != nil {
			m.log.Errorf("could not audit memory usage: %v", err)
		}

		memory_utilization.Set(
			float64(stats.Used) / float64(stats.Total),
		)
	}
}

func (m *MetricsServer) auditDiskWorker() {
	var stat unix.Statfs_t
	for {
		time.Sleep(time.Second * 5)

		err := unix.Statfs("/", &stat)
		if err != nil {
			fmt.Println("could not audit disk usage: ", err)
		}

		disk_utilization.Set(
			float64(stat.Bfree) / float64(stat.Blocks),
		)
	}
}

func ServerMiddleware(next http.Handler) http.Handler {
	return instrumentationMiddleware(next, false)
}

func AgentServerMiddleware(next http.Handler) http.Handler {
	return instrumentationMiddleware(next, true)
}

func instrumentationMiddleware(next http.Handler, agentServer bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		if agentServer {
			agent_traffic.Inc()
		} else {
			api_traffic.Inc()
		}

		lw := NewLoggingResponseWriter(w)
		next.ServeHTTP(lw, r)
		statusClass := lw.statusCode - lw.statusCode%100

		if statusClass == 400 {
			client_errors.Inc()
		}

		if statusClass == 500 {
			server_errors.Inc()
		}

		stop := time.Now()
		this_latency := stop.Sub(start).Seconds()

		if statusClass == 200 && this_latency > slo_max {
			slo_violations.Inc()
		}

		if statusClass == 200 {
			success_latency.Observe(this_latency)
		} else {
			error_latency.Observe(this_latency)
		}
	})
}
