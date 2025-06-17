package instrumentation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.10.0"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	gracefulShutdownTimeout = 5 * time.Second
	readTimeout             = 5 * time.Second
	writeTimeout            = 10 * time.Second
)

type MetricsServer struct {
	log        logrus.FieldLogger
	cfg        *config.Config
	collectors []metrics.NamedCollector
}

func NewMetricsServer(
	log logrus.FieldLogger,
	cfg *config.Config,
	collectors ...metrics.NamedCollector,
) *MetricsServer {
	traced := make([]metrics.NamedCollector, len(collectors))
	for i := range collectors {
		if collectors[i] != nil {
			traced[i] = metrics.WrapWithTrace(collectors[i])
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

	handler := otelhttp.NewHandler(metrics.NewHandler(m.collectors...), "metrics")

	srv := &http.Server{
		Addr:         m.cfg.Metrics.Address,
		Handler:      handler,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
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

// InitTracer initializes OpenTelemetry tracing using the provided config.
// It sets the global TracerProvider, which will be used for all span creation throughout the application.
// If tracing is disabled or misconfigured, a no-op tracer provider is used instead.
//
// The returned shutdown function should be called on application exit to ensure all spans are flushed.
func InitTracer(log logrus.FieldLogger, cfg *config.Config, serviceName string) func(context.Context) error {
	if cfg.Tracing == nil || !cfg.Tracing.Enabled {
		log.Info("Tracing is disabled")
		otel.SetTracerProvider(noop.NewTracerProvider())
		return func(ctx context.Context) error { return nil }
	}

	opts := []otlptracehttp.Option{}

	if cfg.Tracing.Endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpoint(cfg.Tracing.Endpoint))
	}

	if cfg.Tracing.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}

	exp, err := otlptracehttp.New(context.Background(), opts...)
	if err != nil {
		log.Fatalf("Failed to initialize OTLP exporter: %v", err)
	}

	svc := "flightctl"
	if serviceName != "" {
		svc = serviceName
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(svc),
		)),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

	log.Info("Tracing initialized")
	return tp.Shutdown
}
