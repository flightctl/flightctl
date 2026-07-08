package dependencyref

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
	return tracing.StartSpan(ctx, "flightctl/service/dependencyref", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) DeleteDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteDependencyRefsByFleet")
	st := t.inner.DeleteDependencyRefsByFleet(ctx, orgId, fleetName)
	endSpan(span, st)
	return st
}

func (t *TracedService) DeleteDependencyRefsByDevice(ctx context.Context, orgId uuid.UUID, deviceName string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteDependencyRefsByDevice")
	st := t.inner.DeleteDependencyRefsByDevice(ctx, orgId, deviceName)
	endSpan(span, st)
	return st
}

func (t *TracedService) ReplaceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "ReplaceDependencyRefsByFleet")
	st := t.inner.ReplaceDependencyRefsByFleet(ctx, orgId, fleetName, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) ReplaceDeviceDependencyRefsByFleet(ctx context.Context, orgId uuid.UUID, fleetName string, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "ReplaceDeviceDependencyRefsByFleet")
	st := t.inner.ReplaceDeviceDependencyRefsByFleet(ctx, orgId, fleetName, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) ReplaceFleetDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, fleetName, deviceName string, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "ReplaceFleetDeviceDependencyRefs")
	st := t.inner.ReplaceFleetDeviceDependencyRefs(ctx, orgId, fleetName, deviceName, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) ReplaceFleetScopedDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "ReplaceFleetScopedDeviceDependencyRefs")
	st := t.inner.ReplaceFleetScopedDeviceDependencyRefs(ctx, orgId, deviceName, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) ReplaceStandaloneDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, deviceName string, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "ReplaceStandaloneDeviceDependencyRefs")
	st := t.inner.ReplaceStandaloneDeviceDependencyRefs(ctx, orgId, deviceName, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) BulkUpsertDeviceDependencyRefs(ctx context.Context, orgId uuid.UUID, refs []model.DependencyRef) domain.Status {
	ctx, span := startSpan(ctx, "BulkUpsertDeviceDependencyRefs")
	st := t.inner.BulkUpsertDeviceDependencyRefs(ctx, orgId, refs)
	endSpan(span, st)
	return st
}

func (t *TracedService) ListDependencyRefsByRefType(ctx context.Context, orgId uuid.UUID, refType string) ([]model.DependencyRef, domain.Status) {
	ctx, span := startSpan(ctx, "ListDependencyRefsByRefType")
	resp, st := t.inner.ListDependencyRefsByRefType(ctx, orgId, refType)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListDueGitDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.GitDependencyProbe, domain.Status) {
	ctx, span := startSpan(ctx, "ListDueGitDependencies")
	resp, st := t.inner.ListDueGitDependencies(ctx, orgId, pollInterval)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListDueHttpDependencies(ctx context.Context, orgId uuid.UUID, pollInterval time.Duration) ([]model.HttpDependencyProbe, domain.Status) {
	ctx, span := startSpan(ctx, "ListDueHttpDependencies")
	resp, st := t.inner.ListDueHttpDependencies(ctx, orgId, pollInterval)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListSecretDependencyTargets(ctx context.Context, secretNamespace, secretName, newFingerprint string) ([]model.SecretDependencyRef, domain.Status) {
	ctx, span := startSpan(ctx, "ListSecretDependencyTargets")
	resp, st := t.inner.ListSecretDependencyTargets(ctx, secretNamespace, secretName, newFingerprint)
	endSpan(span, st)
	return resp, st
}
