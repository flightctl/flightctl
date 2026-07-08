package repository

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
	return tracing.StartSpan(ctx, "flightctl/service/repository", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateRepository(ctx context.Context, orgId uuid.UUID, repo domain.Repository) (*domain.Repository, domain.Status) {
	ctx, span := startSpan(ctx, "CreateRepository")
	resp, st := t.inner.CreateRepository(ctx, orgId, repo)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListRepositories(ctx context.Context, orgId uuid.UUID, params domain.ListRepositoriesParams) (*domain.RepositoryList, domain.Status) {
	ctx, span := startSpan(ctx, "ListRepositories")
	resp, st := t.inner.ListRepositories(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status) {
	ctx, span := startSpan(ctx, "GetRepository")
	resp, st := t.inner.GetRepository(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repo domain.Repository) (*domain.Repository, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceRepository")
	resp, st := t.inner.ReplaceRepository(ctx, orgId, name, repo)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteRepository")
	st := t.inner.DeleteRepository(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Repository, domain.Status) {
	ctx, span := startSpan(ctx, "PatchRepository")
	resp, st := t.inner.PatchRepository(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository domain.Repository, err error) (*domain.Repository, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceRepositoryStatusByError")
	resp, st := t.inner.ReplaceRepositoryStatusByError(ctx, orgId, name, repository, err)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.FleetList, domain.Status) {
	ctx, span := startSpan(ctx, "GetRepositoryFleetReferences")
	resp, st := t.inner.GetRepositoryFleetReferences(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceList, domain.Status) {
	ctx, span := startSpan(ctx, "GetRepositoryDeviceReferences")
	resp, st := t.inner.GetRepositoryDeviceReferences(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) CheckRepositoryOciTag(ctx context.Context, orgId uuid.UUID, repositoryName, imageName, tag string) (*domain.OciRegistryCheckResult, domain.Status) {
	ctx, span := startSpan(ctx, "CheckRepositoryOciTag")
	resp, st := t.inner.CheckRepositoryOciTag(ctx, orgId, repositoryName, imageName, tag)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) CheckRepositoryOciImage(ctx context.Context, orgId uuid.UUID, repositoryName, imageName string) (*domain.OciRegistryCheckResult, domain.Status) {
	ctx, span := startSpan(ctx, "CheckRepositoryOciImage")
	resp, st := t.inner.CheckRepositoryOciImage(ctx, orgId, repositoryName, imageName)
	endSpan(span, st)
	return resp, st
}
