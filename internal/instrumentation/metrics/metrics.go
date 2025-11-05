package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
)

const (
	httpAddrDefault             = "127.0.0.1:15690"
	httpGracefulShutdownTimeout = 5 * time.Second
	httpReadHeaderTimeout       = 2 * time.Second
	httpReadTimeout             = 5 * time.Second
	httpWriteTimeout            = 10 * time.Second
	httpIdleTimeout             = 60 * time.Second
)

type promLogger struct{ log logrus.FieldLogger }

func newPromLogger(l logrus.FieldLogger) *promLogger { return &promLogger{log: l} }

func (l *promLogger) Println(v ...any) {
	if l.log != nil {
		l.log.Debug(v...)
	}
}

// MetricsServer is an HTTP server exposing a Prometheus /metrics endpoint.
// By default it listens on 127.0.0.1 and serves metrics from the provided collectors.
type MetricsServer struct {
	log        logrus.FieldLogger
	opts       metricsServerOptions
	collectors []prometheus.Collector
}

// MetricsServerOption configures a MetricsServer.
type MetricsServerOption func(*metricsServerOptions)

type metricsServerOptions struct {
	addr           string
	handlerWrapper func(http.Handler) http.Handler
	maxInflight    int
	scrapeTimeout  time.Duration
}

// WithListenAddr sets host:port. Examples: "127.0.0.1:15690", "0.0.0.0:15690", "[::]:15690".
func WithListenAddr(addr string) MetricsServerOption {
	return func(o *metricsServerOptions) {
		if addr != "" {
			o.addr = addr
		}
	}
}

// WithHandlerWrapper injects middleware around /metrics (e.g., OpenTelemetry).
func WithHandlerWrapper(wrap func(http.Handler) http.Handler) MetricsServerOption {
	return func(o *metricsServerOptions) { o.handlerWrapper = wrap }
}

// WithMaxInflight limits concurrent scrapes (0 = unlimited).
func WithMaxInflight(n int) MetricsServerOption {
	return func(o *metricsServerOptions) { o.maxInflight = n }
}

// WithScrapeTimeout sets a server-side cap for a single scrape (0 = no cap).
func WithScrapeTimeout(d time.Duration) MetricsServerOption {
	return func(o *metricsServerOptions) { o.scrapeTimeout = d }
}

// NewMetricsServer creates a Prometheus metrics server with the given logger.
func NewMetricsServer(log logrus.FieldLogger, collectors ...prometheus.Collector) *MetricsServer {
	return &MetricsServer{
		log:        log,
		opts:       defaultMetricsServerOptions(),
		collectors: collectors,
	}
}

// Run starts the metrics HTTP server and blocks until the provided context is canceled
// or an error occurs. Optional settings can be supplied via MetricsServerOption.
func (m *MetricsServer) Run(ctx context.Context, options ...MetricsServerOption) error {
	opts := m.opts
	for _, fn := range options {
		if fn != nil {
			fn(&opts)
		}
	}

	reg := prometheus.NewRegistry()
	for _, c := range m.collectors {
		if err := reg.Register(c); err != nil {
			return fmt.Errorf("register collector: %w", err)
		}
	}

	mux := http.NewServeMux()

	// promhttp.HandlerFor serves the registry; InstrumentMetricHandler adds
	// standard handler-level metrics (e.g., requests total) to the same registry.
	handler := promhttp.InstrumentMetricHandler(
		reg,
		promhttp.HandlerFor(reg, promhttp.HandlerOpts{
			ErrorLog:            newPromLogger(m.log),
			MaxRequestsInFlight: opts.maxInflight,
			Timeout:             opts.scrapeTimeout,
		}),
	)

	// Optional: wrap with tracing/auth/etc.
	if opts.handlerWrapper != nil {
		handler = opts.handlerWrapper(handler)
	}

	mux.Handle("/metrics", handler)

	srv := &http.Server{
		Addr:              opts.addr,
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	go func() {
		<-ctx.Done()
		if m.log != nil {
			m.log.WithError(ctx.Err()).Info("metrics: shutdown signal received")
		}
		ctxTimeout, cancel := context.WithTimeout(context.Background(), httpGracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctxTimeout); err != nil && m.log != nil {
			m.log.WithError(err).Warn("metrics: server shutdown error")
		}
	}()

	if m.log != nil {
		m.log.WithField("addr", opts.addr).Info("metrics: listening")
	}

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func defaultMetricsServerOptions() metricsServerOptions {
	return metricsServerOptions{
		addr:           httpAddrDefault,
		maxInflight:    0,
		scrapeTimeout:  0,
		handlerWrapper: func(h http.Handler) http.Handler { return h },
	}
}
