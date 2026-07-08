package catalog

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
	return tracing.StartSpan(ctx, "flightctl/service/catalog", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedService) CreateCatalog(ctx context.Context, orgId uuid.UUID, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "CreateCatalog")
	resp, st := t.inner.CreateCatalog(ctx, orgId, catalog)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListCatalogs(ctx context.Context, orgId uuid.UUID, params domain.ListCatalogsParams) (*domain.CatalogList, domain.Status) {
	ctx, span := startSpan(ctx, "ListCatalogs")
	resp, st := t.inner.ListCatalogs(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetCatalog(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "GetCatalog")
	resp, st := t.inner.GetCatalog(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceCatalog(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceCatalog")
	resp, st := t.inner.ReplaceCatalog(ctx, orgId, name, catalog)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteCatalog(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteCatalog")
	st := t.inner.DeleteCatalog(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedService) PatchCatalog(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "PatchCatalog")
	resp, st := t.inner.PatchCatalog(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetCatalogStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "GetCatalogStatus")
	resp, st := t.inner.GetCatalogStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, catalog domain.Catalog) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceCatalogStatus")
	resp, st := t.inner.ReplaceCatalogStatus(ctx, orgId, name, catalog)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchCatalogStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Catalog, domain.Status) {
	ctx, span := startSpan(ctx, "PatchCatalogStatus")
	resp, st := t.inner.PatchCatalogStatus(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListAllCatalogItems(ctx context.Context, orgId uuid.UUID, params domain.ListAllCatalogItemsParams) (*domain.CatalogItemList, domain.Status) {
	ctx, span := startSpan(ctx, "ListAllCatalogItems")
	resp, st := t.inner.ListAllCatalogItems(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListCatalogItems(ctx context.Context, orgId uuid.UUID, catalogName string, params domain.ListCatalogItemsParams) (*domain.CatalogItemList, domain.Status) {
	ctx, span := startSpan(ctx, "ListCatalogItems")
	resp, st := t.inner.ListCatalogItems(ctx, orgId, catalogName, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, domain.Status) {
	ctx, span := startSpan(ctx, "GetCatalogItem")
	resp, st := t.inner.GetCatalogItem(ctx, orgId, catalogName, itemName)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) CreateCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	ctx, span := startSpan(ctx, "CreateCatalogItem")
	resp, st := t.inner.CreateCatalogItem(ctx, orgId, catalogName, item)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, item domain.CatalogItem) (*domain.CatalogItem, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceCatalogItem")
	resp, st := t.inner.ReplaceCatalogItem(ctx, orgId, catalogName, itemName, item)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string, patch domain.PatchRequest) (*domain.CatalogItem, domain.Status) {
	ctx, span := startSpan(ctx, "PatchCatalogItem")
	resp, st := t.inner.PatchCatalogItem(ctx, orgId, catalogName, itemName, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteCatalogItem(ctx context.Context, orgId uuid.UUID, catalogName string, itemName string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteCatalogItem")
	st := t.inner.DeleteCatalogItem(ctx, orgId, catalogName, itemName)
	endSpan(span, st)
	return st
}
