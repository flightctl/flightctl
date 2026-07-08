package templateversion

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
	return tracing.StartSpan(ctx, "flightctl/service/templateversion", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateTemplateVersion(ctx context.Context, orgId uuid.UUID, templateVersion domain.TemplateVersion, immediateRollout bool) (*domain.TemplateVersion, domain.Status) {
	ctx, span := startSpan(ctx, "CreateTemplateVersion")
	resp, st := t.inner.CreateTemplateVersion(ctx, orgId, templateVersion, immediateRollout)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListTemplateVersions(ctx context.Context, orgId uuid.UUID, fleet string, params domain.ListTemplateVersionsParams) (*domain.TemplateVersionList, domain.Status) {
	ctx, span := startSpan(ctx, "ListTemplateVersions")
	resp, st := t.inner.ListTemplateVersions(ctx, orgId, fleet, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*domain.TemplateVersion, domain.Status) {
	ctx, span := startSpan(ctx, "GetTemplateVersion")
	resp, st := t.inner.GetTemplateVersion(ctx, orgId, fleet, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteTemplateVersion")
	st := t.inner.DeleteTemplateVersion(ctx, orgId, fleet, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetLatestTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string) (*domain.TemplateVersion, domain.Status) {
	ctx, span := startSpan(ctx, "GetLatestTemplateVersion")
	resp, st := t.inner.GetLatestTemplateVersion(ctx, orgId, fleet)
	endSpan(span, st)
	return resp, st
}
