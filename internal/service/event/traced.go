package event

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/google/uuid"
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
	return tracing.StartSpan(ctx, "flightctl/service/event", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *domain.Event) {
	ctx, span := startSpan(ctx, "CreateEvent")
	t.inner.CreateEvent(ctx, orgId, event)
	span.End()
}

func (t *TracedService) ListEvents(ctx context.Context, orgId uuid.UUID, params domain.ListEventsParams) (*domain.EventList, domain.Status) {
	ctx, span := startSpan(ctx, "ListEvents")
	resp, st := t.inner.ListEvents(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, domain.Status) {
	ctx, span := startSpan(ctx, "DeleteEventsOlderThan")
	resp, st := t.inner.DeleteEventsOlderThan(ctx, cutoffTime)
	endSpan(span, st)
	return resp, st
}
