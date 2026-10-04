package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	prometheus_client "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const (
	metricsShutdownTimeout   = 5 * time.Second
	metricsReadHeaderTimeout = 2 * time.Second
	metricsReadTimeout       = 5 * time.Second
	metricsWriteTimeout      = 10 * time.Second
	metricsIdleTimeout       = 60 * time.Second
)

// metricsRuntime manages the dedicated Prometheus registry, OTel meter
// provider, and HTTP server for internal operational metrics.
//
// Construction does not bind a socket or start a goroutine. Start binds and
// serves the endpoint at the beginning of Service.Run, before extensions or
// sources are started. This keeps New free of long-lived network resources and
// makes every successful Start pair with an idempotent Shutdown.
//
// When metrics are disabled (nil config), newMetricsRuntime returns a
// runtime backed by a no-op meter provider. Start and Shutdown are safe no-ops
// for the HTTP endpoint in that mode.
type metricsRuntime struct {
	provider metric.MeterProvider
	endpoint string
	server   *http.Server
	listener net.Listener
	log      *logrus.Logger

	// shutdownProvider is non-nil only for a real SDK-backed provider.
	shutdownProvider func(ctx context.Context) error

	mu       sync.Mutex
	started  bool
	stopped  bool
	stopOnce sync.Once
	stopErr  error
}

// newMetricsRuntime constructs a metrics runtime from the service
// configuration without binding the configured endpoint.
func newMetricsRuntime(
	cfg *config.MetricsConfig,
	log *logrus.Logger,
) (*metricsRuntime, error) {
	if cfg == nil {
		log.Info("metrics endpoint disabled")
		return &metricsRuntime{
			provider: noop.NewMeterProvider(),
			log:      log,
		}, nil
	}

	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = "localhost:8888"
	}

	// Dedicated registry — no global state.
	registry := prometheus_client.NewRegistry()

	exporter, err := prometheus.New(
		prometheus.WithRegisterer(registry),
	)
	if err != nil {
		return nil, fmt.Errorf("creating Prometheus exporter: %w", err)
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
	)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{},
	))

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: metricsReadHeaderTimeout,
		ReadTimeout:       metricsReadTimeout,
		WriteTimeout:      metricsWriteTimeout,
		IdleTimeout:       metricsIdleTimeout,
	}

	return &metricsRuntime{
		provider:         meterProvider,
		endpoint:         endpoint,
		server:           srv,
		log:              log,
		shutdownProvider: meterProvider.Shutdown,
	}, nil
}

// Start binds and starts serving the metrics endpoint. The returned channel
// receives the terminal Serve result and is then closed. A nil channel means
// metrics are disabled.
func (m *metricsRuntime) Start() (<-chan error, error) {
	if m.server == nil {
		return nil, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.stopped {
		return nil, fmt.Errorf("metrics runtime is already stopped")
	}
	if m.started {
		return nil, fmt.Errorf("metrics runtime is already started")
	}

	listener, err := net.Listen("tcp", m.endpoint)
	if err != nil {
		return nil, fmt.Errorf(
			"metrics endpoint: listen on %s: %w",
			m.endpoint,
			err,
		)
	}

	m.listener = listener
	m.started = true
	serveErrCh := make(chan error, 1)

	go func() {
		err := m.server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			err = nil
		}
		serveErrCh <- err
		close(serveErrCh)
	}()

	m.log.WithField(
		"endpoint",
		listener.Addr().String(),
	).Info("metrics endpoint started")

	return serveErrCh, nil
}

// Shutdown gracefully stops the metrics HTTP server and then shuts down the
// meter provider. It detaches from caller cancellation while retaining context
// values and applies bounded timeouts to both operations.
//
// Shutdown is idempotent and is also safe before Start, which is required when
// Service construction fails after the meter provider has been created.
func (m *metricsRuntime) Shutdown(ctx context.Context) error {
	m.stopOnce.Do(func() {
		m.stopErr = m.shutdown(ctx)
	})
	return m.stopErr
}

func (m *metricsRuntime) shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.stopped = true
	started := m.started
	listener := m.listener
	m.mu.Unlock()

	var errs []error

	if m.server != nil && started {
		m.log.Info("shutting down metrics endpoint")
		m.server.SetKeepAlivesEnabled(false)

		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			metricsShutdownTimeout,
		)
		if err := m.server.Shutdown(shutdownCtx); err != nil {
			errs = append(errs, fmt.Errorf(
				"shutting down metrics server: %w",
				err,
			))
			if closeErr := m.server.Close(); closeErr != nil &&
				!errors.Is(closeErr, http.ErrServerClosed) {
				errs = append(errs, fmt.Errorf(
					"closing metrics server: %w",
					closeErr,
				))
			}
		}
		cancel()

		// Shutdown normally closes listeners registered by Serve. Close the
		// listener explicitly as well to cover a concurrent Start/Shutdown in
		// which Serve has not registered it yet.
		if listener != nil {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				errs = append(errs, fmt.Errorf(
					"closing metrics listener: %w",
					err,
				))
			}
		}
	}

	if m.shutdownProvider != nil {
		providerCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			metricsShutdownTimeout,
		)
		if err := m.shutdownProvider(providerCtx); err != nil {
			errs = append(errs, fmt.Errorf(
				"shutting down meter provider: %w",
				err,
			))
		}
		cancel()
	}

	if m.server != nil && started {
		m.log.Info("metrics endpoint stopped")
	}

	return errors.Join(errs...)
}
