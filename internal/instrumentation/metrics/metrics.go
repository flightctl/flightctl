package metrics

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
)

const (
	contentTypeHeader     = "Content-Type"
	contentEncodingHeader = "Content-Encoding"
	acceptEncodingHeader  = "Accept-Encoding"
)

const (
	httpGracefulShutdownTimeout = 5 * time.Second
	httpReadHeaderTimeout       = 2 * time.Second
	httpReadTimeout             = 5 * time.Second
	httpWriteTimeout            = 10 * time.Second
	httpIdleTimeout             = 60 * time.Second
)

type MetricsServer struct {
	log        logrus.FieldLogger
	cfg        *config.Config
	collectors []NamedCollector
}

func NewMetricsServer(
	log logrus.FieldLogger,
	cfg *config.Config,
	collectors ...NamedCollector,
) *MetricsServer {
	traced := make([]NamedCollector, 0, len(collectors))
	for i := range collectors {
		if collectors[i] != nil {
			traced = append(traced, WrapWithTrace(collectors[i]))
		}
	}

	return &MetricsServer{
		log:        log,
		cfg:        cfg,
		collectors: traced,
	}
}

func (m *MetricsServer) Run(ctx context.Context) error {
	if m.cfg == nil {
		return fmt.Errorf("configuration is nil")
	}
	if m.cfg.Metrics == nil {
		return fmt.Errorf("metrics configuration is missing")
	}
	if !m.cfg.Metrics.Enabled {
		return fmt.Errorf("metrics server is disabled by configuration")
	}

	handler := NewHandler(m.collectors...)

	srv := &http.Server{
		Addr:              m.cfg.Metrics.Address,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
	}

	go func() {
		<-ctx.Done()
		if m.log != nil {
			m.log.WithError(ctx.Err()).Info("Shutdown signal received")
		}
		ctxTimeout, cancel := context.WithTimeout(context.Background(), httpGracefulShutdownTimeout)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctxTimeout); err != nil && m.log != nil {
			m.log.WithError(err).Warn("Metrics server shutdown error")
		}

	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// NamedCollector is a Prometheus collector that also exposes a consistent name
// used for tracing purposes.
type NamedCollector interface {
	prometheus.Collector
	MetricsName() string
}

// ContextAwareCollector allows injecting context into a wrapped collector.
type ContextAwareCollector interface {
	prometheus.Collector
	WithContext(ctx context.Context) NamedCollector
}

// tracedCollector wraps a NamedCollector and adds span tracing during collection.
type tracedCollector struct {
	ctx         context.Context
	collector   NamedCollector
	metricNames []string
}

func (tc *tracedCollector) MetricsName() string {
	return tc.collector.MetricsName()
}

func (tc *tracedCollector) Describe(ch chan<- *prometheus.Desc) {
	tc.collector.Describe(ch)
}

func (tc *tracedCollector) Collect(ch chan<- prometheus.Metric) {
	ctx := ctxOrBackground(tc.ctx)
	_, span := tracing.StartSpan(ctx, "flightctl/metrics", tc.collector.MetricsName())
	defer span.End()

	if len(tc.metricNames) > 20 {
		span.SetAttributes(attribute.Int("collector.metric_count", len(tc.metricNames)))
	} else {
		span.SetAttributes(attribute.StringSlice("collector.metrics", tc.metricNames))
	}

	tc.collector.Collect(ch)
}

func (tc *tracedCollector) WithContext(ctx context.Context) NamedCollector {
	return &tracedCollector{
		ctx:         ctxOrBackground(ctx),
		collector:   tc.collector,
		metricNames: tc.metricNames,
	}
}

// WrapWithTrace wraps a NamedCollector with tracing and precomputes metric descriptor names.
func WrapWithTrace(c NamedCollector) NamedCollector {
	descs := make(chan *prometheus.Desc)
	var metricNames []string
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		localNames := make([]string, 0, 32) // small prealloc
		for d := range descs {
			localNames = append(localNames, d.String())
		}
		metricNames = localNames
	}()

	c.Describe(descs)
	close(descs)
	wg.Wait()

	return &tracedCollector{
		collector:   c,
		metricNames: metricNames,
	}
}

// NewHandler returns an HTTP handler that gathers metrics from the provided NamedCollectors.
// Each collector is wrapped with tracing and the request context (if supported).
func NewHandler(collectors ...NamedCollector) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		registry := prometheus.NewRegistry()

		for _, c := range collectors {
			col := prometheus.Collector(c)
			if ctxAware, ok := c.(ContextAwareCollector); ok {
				col = ctxAware.WithContext(ctx)
			}
			if err := registry.Register(col); err != nil {
				http.Error(w, fmt.Sprintf("failed to register collector: %v", err), http.StatusInternalServerError)
				return
			}
		}

		metrics, err := registry.Gather()
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to gather metrics: %v", err), http.StatusInternalServerError)
			return
		}

		contentType := expfmt.Negotiate(r.Header)
		w.Header().Set(contentTypeHeader, string(contentType))

		var writer io.Writer = w
		if acceptsGzip(r.Header) {
			w.Header().Set(contentEncodingHeader, "gzip")
			gzipWriter := gzip.NewWriter(w)
			defer gzipWriter.Close()
			writer = gzipWriter
		}

		encoder := expfmt.NewEncoder(writer, contentType)
		for _, mf := range metrics {
			if err := encoder.Encode(mf); err != nil {
				http.Error(w, fmt.Sprintf("failed to encode metrics: %v", err), http.StatusInternalServerError)
				return
			}
		}

		if closer, ok := encoder.(expfmt.Closer); ok {
			if err := closer.Close(); err != nil {
				http.Error(w, fmt.Sprintf("failed to flush metrics: %v", err), http.StatusInternalServerError)
			}
		}
	})
}

// ctxOrBackground returns ctx or context.Background if ctx is nil.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

// acceptsGzip returns true if the request header allows gzip encoding.
func acceptsGzip(header http.Header) bool {
	for _, val := range strings.Split(header.Get(acceptEncodingHeader), ",") {
		if part := strings.TrimSpace(val); part == "gzip" || strings.HasPrefix(part, "gzip;") {
			return true
		}
	}
	return false
}
