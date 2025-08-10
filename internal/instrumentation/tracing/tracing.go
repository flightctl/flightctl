package tracing

import (
	"context"

	"github.com/stoewer/go-strcase"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

// StartSpan creates a new span using the global tracer provider.
// It uses the provided context to determine the parent span (if any),
// and returns a new context and the started span.
// The span name is normalized to kebab-case.
func StartSpan(ctx context.Context, tracerName, spanName string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	tracer := otel.GetTracerProvider().Tracer(tracerName)
	return tracer.Start(ctx, strcase.KebabCase(spanName), opts...)
}
