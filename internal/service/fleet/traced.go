// Package fleet's traced.go is a hand-written OTel tracing wrapper. Per-resource sub-packages
// each define their own TracedService with a tracer name of "flightctl/service/{resource}"
// (was the shared constant "flightctl/service" in the monolithic internal/service package);
// the span name stays the bare original Go method name, kebab-cased by tracing.StartSpan
// exactly as today. This convention is established here (and mirrored by every sibling
// sub-package's traced.go) because EDM-4675, which was to formalize per-package codegen
// conventions, has not been implemented as of this story.
package fleet

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
	return tracing.StartSpan(ctx, "flightctl/service/fleet", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateFleet(ctx context.Context, orgId uuid.UUID, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "CreateFleet")
	resp, st := t.inner.CreateFleet(ctx, orgId, fleet)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListFleets(ctx context.Context, orgId uuid.UUID, params domain.ListFleetsParams) (*domain.FleetList, domain.Status) {
	ctx, span := startSpan(ctx, "ListFleets")
	resp, st := t.inner.ListFleets(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetFleet(ctx context.Context, orgId uuid.UUID, name string, params domain.GetFleetParams) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "GetFleet")
	resp, st := t.inner.GetFleet(ctx, orgId, name, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceFleet")
	resp, st := t.inner.ReplaceFleet(ctx, orgId, name, fleet)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteFleet")
	st := t.inner.DeleteFleet(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "GetFleetStatus")
	resp, st := t.inner.GetFleetStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet domain.Fleet) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceFleetStatus")
	resp, st := t.inner.ReplaceFleetStatus(ctx, orgId, name, fleet)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Fleet, domain.Status) {
	ctx, span := startSpan(ctx, "PatchFleet")
	resp, st := t.inner.PatchFleet(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	ctx, span := startSpan(ctx, "ListFleetRolloutDeviceSelection")
	resp, st := t.inner.ListFleetRolloutDeviceSelection(ctx, orgId)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, domain.Status) {
	ctx, span := startSpan(ctx, "ListDisruptionBudgetFleets")
	resp, st := t.inner.ListDisruptionBudgetFleets(ctx, orgId)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	ctx, span := startSpan(ctx, "UpdateFleetConditions")
	st := t.inner.UpdateFleetConditions(ctx, orgId, name, conditions)
	endSpan(span, st)
	return st
}

func (t *TracedService) UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	ctx, span := startSpan(ctx, "UpdateFleetAnnotations")
	st := t.inner.UpdateFleetAnnotations(ctx, orgId, name, annotations, deleteKeys)
	endSpan(span, st)
	return st
}

func (t *TracedService) OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status {
	ctx, span := startSpan(ctx, "OverwriteFleetRepositoryRefs")
	st := t.inner.OverwriteFleetRepositoryRefs(ctx, orgId, name, repositoryNames...)
	endSpan(span, st)
	return st
}

func (t *TracedService) GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status) {
	ctx, span := startSpan(ctx, "GetFleetRepositoryRefs")
	resp, st := t.inner.GetFleetRepositoryRefs(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
