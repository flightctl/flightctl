package tracing

import (
	"context"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"github.com/stoewer/go-strcase"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.28.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

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

// StartSpan creates a new span using the global tracer provider.
// It uses the provided context to determine the parent span (if any),
// and returns a new context and the started span.
// The span name is normalized to kebab-case.
func StartSpan(ctx context.Context, tracerName, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := otel.GetTracerProvider().Tracer(tracerName)
	return tracer.Start(ctx, strcase.KebabCase(spanName), opts...)
}
