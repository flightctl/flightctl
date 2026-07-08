package authprovider

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
	return tracing.StartSpan(ctx, "flightctl/service/authprovider", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "CreateAuthProvider")
	resp, st := t.inner.CreateAuthProvider(ctx, orgId, authProvider)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListAuthProviders(ctx context.Context, orgId uuid.UUID, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status) {
	ctx, span := startSpan(ctx, "ListAuthProviders")
	resp, st := t.inner.ListAuthProviders(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListAllAuthProviders(ctx context.Context, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status) {
	ctx, span := startSpan(ctx, "ListAllAuthProviders")
	resp, st := t.inner.ListAllAuthProviders(ctx, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "GetAuthProvider")
	resp, st := t.inner.GetAuthProvider(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "GetAuthProviderByIssuerAndClientId")
	resp, st := t.inner.GetAuthProviderByIssuerAndClientId(ctx, orgId, issuer, clientId)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "GetAuthProviderByAuthorizationUrl")
	resp, st := t.inner.GetAuthProviderByAuthorizationUrl(ctx, orgId, authorizationUrl)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceAuthProvider")
	resp, st := t.inner.ReplaceAuthProvider(ctx, orgId, name, authProvider)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.AuthProvider, domain.Status) {
	ctx, span := startSpan(ctx, "PatchAuthProvider")
	resp, st := t.inner.PatchAuthProvider(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteAuthProvider")
	st := t.inner.DeleteAuthProvider(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetAuthConfig(ctx context.Context, authConfig *domain.AuthConfig) (*domain.AuthConfig, domain.Status) {
	ctx, span := startSpan(ctx, "GetAuthConfig")
	resp, st := t.inner.GetAuthConfig(ctx, authConfig)
	endSpan(span, st)
	return resp, st
}
