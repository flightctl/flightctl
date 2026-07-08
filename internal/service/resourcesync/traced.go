package resourcesync

import (
	"context"
	"errors"

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
	return tracing.StartSpan(ctx, "flightctl/service/resourcesync", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	ctx, span := startSpan(ctx, "CreateResourceSync")
	resp, st := t.inner.CreateResourceSync(ctx, orgId, rs)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params domain.ListResourceSyncsParams) (*domain.ResourceSyncList, domain.Status) {
	ctx, span := startSpan(ctx, "ListResourceSyncs")
	resp, st := t.inner.ListResourceSyncs(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*domain.ResourceSync, domain.Status) {
	ctx, span := startSpan(ctx, "GetResourceSync")
	resp, st := t.inner.GetResourceSync(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceResourceSync")
	resp, st := t.inner.ReplaceResourceSync(ctx, orgId, name, rs)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteResourceSync")
	st := t.inner.DeleteResourceSync(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.ResourceSync, domain.Status) {
	ctx, span := startSpan(ctx, "PatchResourceSync")
	resp, st := t.inner.PatchResourceSync(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync domain.ResourceSync) (*domain.ResourceSync, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceResourceSyncStatus")
	resp, st := t.inner.ReplaceResourceSyncStatus(ctx, orgId, name, resourceSync)
	endSpan(span, st)
	return resp, st
}
