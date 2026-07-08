package enrollmentrequest

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
	return tracing.StartSpan(ctx, "flightctl/service/enrollmentrequest", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "CreateEnrollmentRequest")
	resp, st := t.inner.CreateEnrollmentRequest(ctx, orgId, er)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, params domain.ListEnrollmentRequestsParams) (*domain.EnrollmentRequestList, domain.Status) {
	ctx, span := startSpan(ctx, "ListEnrollmentRequests")
	resp, st := t.inner.ListEnrollmentRequests(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "GetEnrollmentRequest")
	resp, st := t.inner.GetEnrollmentRequest(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceEnrollmentRequest")
	resp, st := t.inner.ReplaceEnrollmentRequest(ctx, orgId, name, er)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "PatchEnrollmentRequest")
	resp, st := t.inner.PatchEnrollmentRequest(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteEnrollmentRequest")
	st := t.inner.DeleteEnrollmentRequest(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "GetEnrollmentRequestStatus")
	resp, st := t.inner.GetEnrollmentRequestStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ApproveEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, approval domain.EnrollmentRequestApproval) (*domain.EnrollmentRequestApprovalStatus, domain.Status) {
	ctx, span := startSpan(ctx, "ApproveEnrollmentRequest")
	resp, st := t.inner.ApproveEnrollmentRequest(ctx, orgId, name, approval)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er domain.EnrollmentRequest) (*domain.EnrollmentRequest, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceEnrollmentRequestStatus")
	resp, st := t.inner.ReplaceEnrollmentRequestStatus(ctx, orgId, name, er)
	endSpan(span, st)
	return resp, st
}
