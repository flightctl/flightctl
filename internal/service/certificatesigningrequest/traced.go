package certificatesigningrequest

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
	return tracing.StartSpan(ctx, "flightctl/service/certificatesigningrequest", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params domain.ListCertificateSigningRequestsParams) (*domain.CertificateSigningRequestList, domain.Status) {
	ctx, span := startSpan(ctx, "ListCertificateSigningRequests")
	resp, st := t.inner.ListCertificateSigningRequests(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	ctx, span := startSpan(ctx, "CreateCertificateSigningRequest")
	resp, st := t.inner.CreateCertificateSigningRequest(ctx, orgId, csr)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteCertificateSigningRequest")
	st := t.inner.DeleteCertificateSigningRequest(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*domain.CertificateSigningRequest, domain.Status) {
	ctx, span := startSpan(ctx, "GetCertificateSigningRequest")
	resp, st := t.inner.GetCertificateSigningRequest(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.CertificateSigningRequest, domain.Status) {
	ctx, span := startSpan(ctx, "PatchCertificateSigningRequest")
	resp, st := t.inner.PatchCertificateSigningRequest(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceCertificateSigningRequest")
	resp, st := t.inner.ReplaceCertificateSigningRequest(ctx, orgId, name, csr)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr domain.CertificateSigningRequest) (*domain.CertificateSigningRequest, domain.Status) {
	ctx, span := startSpan(ctx, "UpdateCertificateSigningRequestApproval")
	resp, st := t.inner.UpdateCertificateSigningRequestApproval(ctx, orgId, name, csr)
	endSpan(span, st)
	return resp, st
}
