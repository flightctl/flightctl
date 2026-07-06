// Package device's traced.go is a hand-written OTel tracing wrapper. Per-resource sub-packages
// each define their own tracing wrapper with a tracer name of "flightctl/service/{resource}"
// (was the shared constant "flightctl/service" in the monolithic internal/service package);
// the span name stays the bare original Go method name, kebab-cased by tracing.StartSpan
// exactly as today. This convention was established by internal/service/fleet/traced.go and is
// mirrored here, since EDM-4675 (which was to formalize per-package codegen conventions) has
// not landed as of this story.
package device

import (
	"context"
	"errors"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// TracedDeviceService wraps a Service implementation with OpenTelemetry tracing.
type TracedDeviceService struct {
	inner Service
}

// WrapWithTracing returns a Service that wraps inner with tracing spans, or nil if inner is nil.
func WrapWithTracing(inner Service) Service {
	if inner == nil {
		return nil
	}
	return &TracedDeviceService{inner: inner}
}

func startSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	return tracing.StartSpan(ctx, "flightctl/service/device", method)
}

func endSpan(span trace.Span, st domain.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

func (t *TracedDeviceService) CreateDevice(ctx context.Context, orgId uuid.UUID, device domain.Device) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "CreateDevice")
	resp, st := t.inner.CreateDevice(ctx, orgId, device)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) ListDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DeviceList, domain.Status) {
	ctx, span := startSpan(ctx, "ListDevices")
	resp, st := t.inner.ListDevices(ctx, orgId, params, annotationSelector)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*domain.DeviceList, domain.Status) {
	ctx, span := startSpan(ctx, "ListDevicesByServiceCondition")
	resp, st := t.inner.ListDevicesByServiceCondition(ctx, orgId, conditionType, conditionStatus, listParams)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error) {
	ctx, span := startSpan(ctx, "UpdateDevice")
	resp, err := t.inner.UpdateDevice(ctx, orgId, name, device, fieldsToUnset)
	st := domain.StatusOK()
	if err != nil {
		st = domain.StatusInternalServerError(err.Error())
	}
	endSpan(span, st)
	return resp, err
}

func (t *TracedDeviceService) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "GetDevice")
	resp, st := t.inner.GetDevice(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceDevice")
	resp, st := t.inner.ReplaceDevice(ctx, orgId, name, device, fieldsToUnset)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) DeleteDevice(ctx context.Context, orgId uuid.UUID, name string) domain.Status {
	ctx, span := startSpan(ctx, "DeleteDevice")
	st := t.inner.DeleteDevice(ctx, orgId, name)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) GetDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "GetDeviceStatus")
	resp, st := t.inner.GetDeviceStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) GetDeviceLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*domain.DeviceLastSeen, domain.Status) {
	ctx, span := startSpan(ctx, "GetDeviceLastSeen")
	resp, st := t.inner.GetDeviceLastSeen(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) ReplaceDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, device domain.Device) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "ReplaceDeviceStatus")
	resp, st := t.inner.ReplaceDeviceStatus(ctx, orgId, name, device)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "PatchDeviceStatus")
	resp, st := t.inner.PatchDeviceStatus(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) GetRenderedDevice(ctx context.Context, orgId uuid.UUID, name string, params domain.GetRenderedDeviceParams) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "GetRenderedDevice")
	resp, st := t.inner.GetRenderedDevice(ctx, orgId, name, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "PatchDevice")
	resp, st := t.inner.PatchDevice(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom domain.DeviceDecommission) (*domain.Device, domain.Status) {
	ctx, span := startSpan(ctx, "DecommissionDevice")
	resp, st := t.inner.DecommissionDevice(ctx, orgId, name, decom)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) ResumeDevices(ctx context.Context, orgId uuid.UUID, request domain.DeviceResumeRequest) (domain.DeviceResumeResponse, domain.Status) {
	ctx, span := startSpan(ctx, "ResumeDevices")
	resp, st := t.inner.ResumeDevices(ctx, orgId, request)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) domain.Status {
	ctx, span := startSpan(ctx, "UpdateDeviceAnnotations")
	st := t.inner.UpdateDeviceAnnotations(ctx, orgId, name, annotations, deleteKeys)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string, configFingerprints []domain.DependencySyncConfigRefStatus) domain.Status {
	ctx, span := startSpan(ctx, "UpdateRenderedDevice")
	st := t.inner.UpdateRenderedDevice(ctx, orgId, name, renderedConfig, renderedApplications, specHash, configFingerprints)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition) domain.Status {
	ctx, span := startSpan(ctx, "SetDeviceServiceConditions")
	st := t.inner.SetDeviceServiceConditions(ctx, orgId, name, conditions)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) OverwriteDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) domain.Status {
	ctx, span := startSpan(ctx, "OverwriteDeviceRepositoryRefs")
	st := t.inner.OverwriteDeviceRepositoryRefs(ctx, orgId, name, repositoryNames...)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) GetDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, domain.Status) {
	ctx, span := startSpan(ctx, "GetDeviceRepositoryRefs")
	resp, st := t.inner.GetDeviceRepositoryRefs(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) CountDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, domain.Status) {
	ctx, span := startSpan(ctx, "CountDevices")
	resp, st := t.inner.CountDevices(ctx, orgId, params, annotationSelector)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) UnmarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) domain.Status {
	ctx, span := startSpan(ctx, "UnmarkDevicesRolloutSelection")
	st := t.inner.UnmarkDevicesRolloutSelection(ctx, orgId, fleetName)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) MarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) domain.Status {
	ctx, span := startSpan(ctx, "MarkDevicesRolloutSelection")
	st := t.inner.MarkDevicesRolloutSelection(ctx, orgId, params, annotationSelector, limit)
	endSpan(span, st)
	return st
}

func (t *TracedDeviceService) GetDeviceCompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]domain.DeviceCompletionCount, domain.Status) {
	ctx, span := startSpan(ctx, "GetDeviceCompletionCounts")
	resp, st := t.inner.GetDeviceCompletionCounts(ctx, orgId, owner, templateVersion, updateTimeout)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) CountDevicesByLabels(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, domain.Status) {
	ctx, span := startSpan(ctx, "CountDevicesByLabels")
	resp, st := t.inner.CountDevicesByLabels(ctx, orgId, params, annotationSelector, groupBy)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) GetDevicesSummary(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*domain.DevicesSummary, domain.Status) {
	ctx, span := startSpan(ctx, "GetDevicesSummary")
	resp, st := t.inner.GetDevicesSummary(ctx, orgId, params, annotationSelector)
	endSpan(span, st)
	return resp, st
}

func (t *TracedDeviceService) UpdateServiceSideDeviceStatus(ctx context.Context, orgId uuid.UUID, device domain.Device) bool {
	ctx, span := startSpan(ctx, "UpdateServiceSideDeviceStatus")
	changed := t.inner.UpdateServiceSideDeviceStatus(ctx, orgId, device)
	endSpan(span, domain.StatusOK())
	return changed
}

func (t *TracedDeviceService) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	ctx, span := startSpan(ctx, "SetOutOfDate")
	err := t.inner.SetOutOfDate(ctx, orgId, owner)
	st := domain.StatusOK()
	if err != nil {
		st = domain.StatusInternalServerError(err.Error())
	}
	endSpan(span, st)
	return err
}

func (t *TracedDeviceService) UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error {
	ctx, span := startSpan(ctx, "UpdateServerSideDeviceStatus")
	err := t.inner.UpdateServerSideDeviceStatus(ctx, orgId, name)
	st := domain.StatusOK()
	if err != nil {
		st = domain.StatusInternalServerError(err.Error())
	}
	endSpan(span, st)
	return err
}

func (t *TracedDeviceService) ListConnectivityChangedDevices(ctx context.Context, orgId uuid.UUID, params domain.ListDevicesParams, cutoffTime time.Time) (*domain.DeviceList, domain.Status) {
	ctx, span := startSpan(ctx, "ListConnectivityChangedDevices")
	resp, st := t.inner.ListConnectivityChangedDevices(ctx, orgId, params, cutoffTime)
	endSpan(span, st)
	return resp, st
}
