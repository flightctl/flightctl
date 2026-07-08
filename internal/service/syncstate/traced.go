package syncstate

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store/model"
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
	return tracing.StartSpan(ctx, "flightctl/service/syncstate", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) GetSyncState(ctx context.Context, orgId uuid.UUID, resourceKey string) (*model.SyncState, domain.Status) {
	ctx, span := startSpan(ctx, "GetSyncState")
	resp, st := t.inner.GetSyncState(ctx, orgId, resourceKey)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) SetSyncState(ctx context.Context, orgId uuid.UUID, state *model.SyncState) domain.Status {
	ctx, span := startSpan(ctx, "SetSyncState")
	st := t.inner.SetSyncState(ctx, orgId, state)
	endSpan(span, st)
	return st
}

func (t *TracedService) SetSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKey string, tm time.Time) domain.Status {
	ctx, span := startSpan(ctx, "SetSyncStateLastCheckedAt")
	st := t.inner.SetSyncStateLastCheckedAt(ctx, orgId, resourceKey, tm)
	endSpan(span, st)
	return st
}

func (t *TracedService) BulkUpsertSyncState(ctx context.Context, orgId uuid.UUID, states []model.SyncState) domain.Status {
	ctx, span := startSpan(ctx, "BulkUpsertSyncState")
	st := t.inner.BulkUpsertSyncState(ctx, orgId, states)
	endSpan(span, st)
	return st
}

func (t *TracedService) BulkUpdateSyncStateLastCheckedAt(ctx context.Context, orgId uuid.UUID, resourceKeys []string, tm time.Time) domain.Status {
	ctx, span := startSpan(ctx, "BulkUpdateSyncStateLastCheckedAt")
	st := t.inner.BulkUpdateSyncStateLastCheckedAt(ctx, orgId, resourceKeys, tm)
	endSpan(span, st)
	return st
}
