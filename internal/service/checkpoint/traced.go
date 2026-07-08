package checkpoint

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracedService wraps a Service implementation with OpenTelemetry tracing.
type TracedService struct {
	inner Service
}

// WrapWithTracing returns a Service that wraps inner with tracing spans, or nil if inner is nil.
func WrapWithTracing(inner Service) Service {
	if inner == nil {
		return nil
	}
	return &TracedService{inner: inner}
}

func startSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	return tracing.StartSpan(ctx, "flightctl/service/checkpoint", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, domain.Status) {
	ctx, span := startSpan(ctx, "GetCheckpoint")
	resp, st := t.inner.GetCheckpoint(ctx, consumer, key)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) domain.Status {
	ctx, span := startSpan(ctx, "SetCheckpoint")
	st := t.inner.SetCheckpoint(ctx, consumer, key, value)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetDatabaseTime(ctx context.Context) (time.Time, domain.Status) {
	ctx, span := startSpan(ctx, "GetDatabaseTime")
	resp, st := t.inner.GetDatabaseTime(ctx)
	endSpan(span, st)
	return resp, st
}
