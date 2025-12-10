// Code generated for wrapping service methods with OpenTelemetry tracing

package service

import (
	"context"
	"errors"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type TracedService struct {
	inner Service
}

func WrapWithTracing(svc Service) Service {
	if svc == nil {
		return nil
	}
	return &TracedService{inner: svc}
}

func startSpan(ctx context.Context, method string) (context.Context, trace.Span) {
	ctx, span := tracing.StartSpan(ctx, "flightctl/service", method)
	return ctx, span
}

func endSpan(span trace.Span, st api.Status) {
	span.SetAttributes(attribute.Int("status.code", int(st.Code)))

	if st.Status != "Success" {
		span.RecordError(errors.New(st.Message))
		span.SetStatus(codes.Error, st.Message)
	}

	span.End()
}

// --- CertificateSigningRequest ---
func (t *TracedService) ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, p api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	ctx, span := startSpan(ctx, "ListCertificateSigningRequests")
	resp, st := t.inner.ListCertificateSigningRequests(ctx, orgId, p)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, span := startSpan(ctx, "CreateCertificateSigningRequest")
	resp, st := t.inner.CreateCertificateSigningRequest(ctx, orgId, csr)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteCertificateSigningRequest")
	st := t.inner.DeleteCertificateSigningRequest(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.CertificateSigningRequest, api.Status) {
	ctx, span := startSpan(ctx, "GetCertificateSigningRequest")
	resp, st := t.inner.GetCertificateSigningRequest(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, span := startSpan(ctx, "PatchCertificateSigningRequest")
	resp, st := t.inner.PatchCertificateSigningRequest(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceCertificateSigningRequest")
	resp, st := t.inner.ReplaceCertificateSigningRequest(ctx, orgId, name, csr)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, span := startSpan(ctx, "UpdateCertificateSigningRequestApproval")
	resp, st := t.inner.UpdateCertificateSigningRequestApproval(ctx, orgId, name, csr)
	endSpan(span, st)
	return resp, st
}

// --- Device ---
func (t *TracedService) CreateDevice(ctx context.Context, orgId uuid.UUID, d api.Device) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "CreateDevice")
	resp, st := t.inner.CreateDevice(ctx, orgId, d)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error {
	ctx, span := startSpan(ctx, "UpdateToOutOfDateByOwner")
	defer span.End()
	return t.inner.SetOutOfDate(ctx, orgId, owner)
}

func (t *TracedService) UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error {
	ctx, span := startSpan(ctx, "UpdateServerSideDeviceStatus")
	defer span.End()
	return t.inner.UpdateServerSideDeviceStatus(ctx, orgId, name)
}

func (t *TracedService) ListDevices(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DeviceList, api.Status) {
	ctx, span := startSpan(ctx, "ListDevices")
	resp, st := t.inner.ListDevices(ctx, orgId, params, annotationSelector)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListDisconnectedDevices(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, cutoffTime time.Time) (*api.DeviceList, api.Status) {
	ctx, span := startSpan(ctx, "ListDisconnectedDevices")
	resp, st := t.inner.ListDisconnectedDevices(ctx, orgId, params, cutoffTime)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, api.Status) {
	ctx, span := startSpan(ctx, "ListDevicesByServiceCondition")
	resp, st := t.inner.ListDevicesByServiceCondition(ctx, orgId, conditionType, conditionStatus, listParams)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device api.Device, fieldsToUnset []string) (*api.Device, error) {
	ctx, span := startSpan(ctx, "UpdateDevice")
	resp, err := t.inner.UpdateDevice(ctx, orgId, name, device, fieldsToUnset)
	endSpan(span, StoreErrorToApiStatus(err, false, api.DeviceKind, device.Metadata.Name))
	return resp, err
}
func (t *TracedService) GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "GetDevice")
	resp, st := t.inner.GetDevice(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device api.Device, unset []string) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceDevice")
	resp, st := t.inner.ReplaceDevice(ctx, orgId, name, device, unset)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteDevice(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteDevice")
	st := t.inner.DeleteDevice(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "GetDeviceStatus")
	resp, st := t.inner.GetDeviceStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetDeviceLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceLastSeen, api.Status) {
	ctx, span := startSpan(ctx, "GetDeviceLastSeen")
	resp, st := t.inner.GetDeviceLastSeen(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, device api.Device) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceDeviceStatus")
	resp, st := t.inner.ReplaceDeviceStatus(ctx, orgId, name, device)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "PatchDeviceStatus")
	resp, st := t.inner.PatchDeviceStatus(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetRenderedDevice(ctx context.Context, orgId uuid.UUID, name string, p api.GetRenderedDeviceParams) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "GetRenderedDevice")
	resp, st := t.inner.GetRenderedDevice(ctx, orgId, name, p)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "PatchDevice")
	resp, st := t.inner.PatchDevice(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom api.DeviceDecommission) (*api.Device, api.Status) {
	ctx, span := startSpan(ctx, "DecommissionDevice")
	resp, st := t.inner.DecommissionDevice(ctx, orgId, name, decom)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ResumeDevices(ctx context.Context, orgId uuid.UUID, request api.DeviceResumeRequest) (api.DeviceResumeResponse, api.Status) {
	ctx, span := startSpan(ctx, "ResumeDevices")
	resp, st := t.inner.ResumeDevices(ctx, orgId, request)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) api.Status {
	ctx, span := startSpan(ctx, "UpdateDeviceAnnotations")
	st := t.inner.UpdateDeviceAnnotations(ctx, orgId, name, annotations, deleteKeys)
	endSpan(span, st)
	return st
}
func (t *TracedService) UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApps, specHash string) api.Status {
	ctx, span := startSpan(ctx, "UpdateRenderedDevice")
	st := t.inner.UpdateRenderedDevice(ctx, orgId, name, renderedConfig, renderedApps, specHash)
	endSpan(span, st)
	return st
}
func (t *TracedService) SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) api.Status {
	ctx, span := startSpan(ctx, "SetDeviceServiceConditions")
	st := t.inner.SetDeviceServiceConditions(ctx, orgId, name, conditions)
	endSpan(span, st)
	return st
}
func (t *TracedService) OverwriteDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, refs ...string) api.Status {
	ctx, span := startSpan(ctx, "OverwriteDeviceRepositoryRefs")
	st := t.inner.OverwriteDeviceRepositoryRefs(ctx, orgId, name, refs...)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, api.Status) {
	ctx, span := startSpan(ctx, "GetDeviceRepositoryRefs")
	resp, st := t.inner.GetDeviceRepositoryRefs(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) CountDevices(ctx context.Context, orgId uuid.UUID, p api.ListDevicesParams, sel *selector.AnnotationSelector) (int64, api.Status) {
	ctx, span := startSpan(ctx, "CountDevices")
	resp, st := t.inner.CountDevices(ctx, orgId, p, sel)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UnmarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) api.Status {
	ctx, span := startSpan(ctx, "UnmarkDevicesRolloutSelection")
	st := t.inner.UnmarkDevicesRolloutSelection(ctx, orgId, fleetName)
	endSpan(span, st)
	return st
}
func (t *TracedService) MarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, p api.ListDevicesParams, sel *selector.AnnotationSelector, limit *int) api.Status {
	ctx, span := startSpan(ctx, "MarkDevicesRolloutSelection")
	st := t.inner.MarkDevicesRolloutSelection(ctx, orgId, p, sel, limit)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetDeviceCompletionCounts(ctx context.Context, orgId uuid.UUID, owner, version string, timeout *time.Duration) ([]api.DeviceCompletionCount, api.Status) {
	ctx, span := startSpan(ctx, "GetDeviceCompletionCounts")
	resp, st := t.inner.GetDeviceCompletionCounts(ctx, orgId, owner, version, timeout)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) CountDevicesByLabels(ctx context.Context, orgId uuid.UUID, p api.ListDevicesParams, sel *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status) {
	ctx, span := startSpan(ctx, "CountDevicesByLabels")
	resp, st := t.inner.CountDevicesByLabels(ctx, orgId, p, sel, groupBy)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetDevicesSummary(ctx context.Context, orgId uuid.UUID, p api.ListDevicesParams, sel *selector.AnnotationSelector) (*api.DevicesSummary, api.Status) {
	ctx, span := startSpan(ctx, "GetDevicesSummary")
	resp, st := t.inner.GetDevicesSummary(ctx, orgId, p, sel)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UpdateServiceSideDeviceStatus(ctx context.Context, orgId uuid.UUID, device api.Device) bool {
	ctx, span := startSpan(ctx, "UpdateServiceSideDeviceStatus")
	resp := t.inner.UpdateServiceSideDeviceStatus(ctx, orgId, device)
	endSpan(span, StoreErrorToApiStatus(nil, false, api.DeviceKind, device.Metadata.Name))
	return resp
}

// --- EnrollmentConfig ---
func (t *TracedService) GetEnrollmentConfig(ctx context.Context, orgId uuid.UUID, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status) {
	ctx, span := startSpan(ctx, "GetEnrollmentConfig")
	resp, st := t.inner.GetEnrollmentConfig(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

// --- EnrollmentRequest ---
func (t *TracedService) CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "CreateEnrollmentRequest")
	resp, st := t.inner.CreateEnrollmentRequest(ctx, orgId, er)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status) {
	ctx, span := startSpan(ctx, "ListEnrollmentRequests")
	resp, st := t.inner.ListEnrollmentRequests(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "GetEnrollmentRequest")
	resp, st := t.inner.GetEnrollmentRequest(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceEnrollmentRequest")
	resp, st := t.inner.ReplaceEnrollmentRequest(ctx, orgId, name, er)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) PatchEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "PatchEnrollmentRequest")
	resp, st := t.inner.PatchEnrollmentRequest(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteEnrollmentRequest")
	st := t.inner.DeleteEnrollmentRequest(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "GetEnrollmentRequestStatus")
	resp, st := t.inner.GetEnrollmentRequestStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ApproveEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status) {
	ctx, span := startSpan(ctx, "ApproveEnrollmentRequest")
	resp, st := t.inner.ApproveEnrollmentRequest(ctx, orgId, name, approval)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceEnrollmentRequestStatus")
	resp, st := t.inner.ReplaceEnrollmentRequestStatus(ctx, orgId, name, er)
	endSpan(span, st)
	return resp, st
}

// --- Fleet ---
func (t *TracedService) CreateFleet(ctx context.Context, orgId uuid.UUID, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "CreateFleet")
	resp, st := t.inner.CreateFleet(ctx, orgId, fleet)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListFleets(ctx context.Context, orgId uuid.UUID, params api.ListFleetsParams) (*api.FleetList, api.Status) {
	ctx, span := startSpan(ctx, "ListFleets")
	resp, st := t.inner.ListFleets(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetFleet(ctx context.Context, orgId uuid.UUID, name string, params api.GetFleetParams) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "GetFleet")
	resp, st := t.inner.GetFleet(ctx, orgId, name, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceFleet")
	resp, st := t.inner.ReplaceFleet(ctx, orgId, name, fleet)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteFleet")
	st := t.inner.DeleteFleet(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "GetFleetStatus")
	resp, st := t.inner.GetFleetStatus(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceFleetStatus")
	resp, st := t.inner.ReplaceFleetStatus(ctx, orgId, name, fleet)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Fleet, api.Status) {
	ctx, span := startSpan(ctx, "PatchFleet")
	resp, st := t.inner.PatchFleet(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*api.FleetList, api.Status) {
	ctx, span := startSpan(ctx, "ListFleetRolloutDeviceSelection")
	resp, st := t.inner.ListFleetRolloutDeviceSelection(ctx, orgId)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*api.FleetList, api.Status) {
	ctx, span := startSpan(ctx, "ListDisruptionBudgetFleets")
	resp, st := t.inner.ListDisruptionBudgetFleets(ctx, orgId)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) api.Status {
	ctx, span := startSpan(ctx, "UpdateFleetConditions")
	st := t.inner.UpdateFleetConditions(ctx, orgId, name, conditions)
	endSpan(span, st)
	return st
}
func (t *TracedService) UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) api.Status {
	ctx, span := startSpan(ctx, "UpdateFleetAnnotations")
	st := t.inner.UpdateFleetAnnotations(ctx, orgId, name, annotations, deleteKeys)
	endSpan(span, st)
	return st
}
func (t *TracedService) OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) api.Status {
	ctx, span := startSpan(ctx, "OverwriteFleetRepositoryRefs")
	st := t.inner.OverwriteFleetRepositoryRefs(ctx, orgId, name, repositoryNames...)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, api.Status) {
	ctx, span := startSpan(ctx, "GetFleetRepositoryRefs")
	resp, st := t.inner.GetFleetRepositoryRefs(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

// Additional components (Labels, Repository, ResourceSync, TemplateVersion) to be appended next.

// --- Labels ---
func (t *TracedService) ListLabels(ctx context.Context, orgId uuid.UUID, params api.ListLabelsParams) (*api.LabelList, api.Status) {
	ctx, span := startSpan(ctx, "ListLabels")
	resp, st := t.inner.ListLabels(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

// --- Repository ---
func (t *TracedService) CreateRepository(ctx context.Context, orgId uuid.UUID, repo api.Repository) (*api.Repository, api.Status) {
	ctx, span := startSpan(ctx, "CreateRepository")
	resp, st := t.inner.CreateRepository(ctx, orgId, repo)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListRepositories(ctx context.Context, orgId uuid.UUID, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	ctx, span := startSpan(ctx, "ListRepositories")
	resp, st := t.inner.ListRepositories(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, api.Status) {
	ctx, span := startSpan(ctx, "GetRepository")
	resp, st := t.inner.GetRepository(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repo api.Repository) (*api.Repository, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceRepository")
	resp, st := t.inner.ReplaceRepository(ctx, orgId, name, repo)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteRepository")
	st := t.inner.DeleteRepository(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Repository, api.Status) {
	ctx, span := startSpan(ctx, "PatchRepository")
	resp, st := t.inner.PatchRepository(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository api.Repository, err error) (*api.Repository, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceRepositoryStatusByError")
	resp, st := t.inner.ReplaceRepositoryStatusByError(ctx, orgId, name, repository, err)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, api.Status) {
	ctx, span := startSpan(ctx, "GetRepositoryFleetReferences")
	resp, st := t.inner.GetRepositoryFleetReferences(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, api.Status) {
	ctx, span := startSpan(ctx, "GetRepositoryDeviceReferences")
	resp, st := t.inner.GetRepositoryDeviceReferences(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

// --- ResourceSync ---
func (t *TracedService) CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, span := startSpan(ctx, "CreateResourceSync")
	resp, st := t.inner.CreateResourceSync(ctx, orgId, rs)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	ctx, span := startSpan(ctx, "ListResourceSyncs")
	resp, st := t.inner.ListResourceSyncs(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, api.Status) {
	ctx, span := startSpan(ctx, "GetResourceSync")
	resp, st := t.inner.GetResourceSync(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceResourceSync")
	resp, st := t.inner.ReplaceResourceSync(ctx, orgId, name, rs)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteResourceSync")
	st := t.inner.DeleteResourceSync(ctx, orgId, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	ctx, span := startSpan(ctx, "PatchResourceSync")
	resp, st := t.inner.PatchResourceSync(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceResourceSyncStatus")
	resp, st := t.inner.ReplaceResourceSyncStatus(ctx, orgId, name, resourceSync)
	endSpan(span, st)
	return resp, st
}

// --- TemplateVersion ---
func (t *TracedService) CreateTemplateVersion(ctx context.Context, orgId uuid.UUID, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
	ctx, span := startSpan(ctx, "CreateTemplateVersion")
	resp, st := t.inner.CreateTemplateVersion(ctx, orgId, tv, immediateRollout)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) ListTemplateVersions(ctx context.Context, orgId uuid.UUID, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status) {
	ctx, span := startSpan(ctx, "ListTemplateVersions")
	resp, st := t.inner.ListTemplateVersions(ctx, orgId, fleet, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) GetTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, api.Status) {
	ctx, span := startSpan(ctx, "GetTemplateVersion")
	resp, st := t.inner.GetTemplateVersion(ctx, orgId, fleet, name)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteTemplateVersion")
	st := t.inner.DeleteTemplateVersion(ctx, orgId, fleet, name)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetLatestTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, api.Status) {
	ctx, span := startSpan(ctx, "GetLatestTemplateVersion")
	resp, st := t.inner.GetLatestTemplateVersion(ctx, orgId, fleet)
	endSpan(span, st)
	return resp, st
}

// --- Event ---
func (t *TracedService) CreateEvent(ctx context.Context, orgId uuid.UUID, event *api.Event) {
	ctx, span := startSpan(ctx, "CreateEvent")
	t.inner.CreateEvent(ctx, orgId, event)
	span.End()
}
func (t *TracedService) ListEvents(ctx context.Context, orgId uuid.UUID, params api.ListEventsParams) (*api.EventList, api.Status) {
	ctx, span := startSpan(ctx, "ListEvents")
	resp, st := t.inner.ListEvents(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status) {
	ctx, span := startSpan(ctx, "DeleteEventsOlderThan")
	resp, st := t.inner.DeleteEventsOlderThan(ctx, cutoffTime)
	endSpan(span, st)
	return resp, st
}

// --- Checkpoint ---
func (t *TracedService) GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, api.Status) {
	ctx, span := startSpan(ctx, "GetCheckpoint")
	resp, st := t.inner.GetCheckpoint(ctx, consumer, key)
	endSpan(span, st)
	return resp, st
}
func (t *TracedService) SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) api.Status {
	ctx, span := startSpan(ctx, "SetCheckpoint")
	st := t.inner.SetCheckpoint(ctx, consumer, key, value)
	endSpan(span, st)
	return st
}
func (t *TracedService) GetDatabaseTime(ctx context.Context) (time.Time, api.Status) {
	ctx, span := startSpan(ctx, "GetDatabaseTime")
	resp, st := t.inner.GetDatabaseTime(ctx)
	endSpan(span, st)
	return resp, st
}

// --- Organization ---
func (t *TracedService) ListOrganizations(ctx context.Context, params api.ListOrganizationsParams) (*api.OrganizationList, api.Status) {
	ctx, span := startSpan(ctx, "ListOrganizations")
	resp, st := t.inner.ListOrganizations(ctx, params)
	endSpan(span, st)
	return resp, st
}

// --- AuthProvider ---
func (t *TracedService) CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "CreateAuthProvider")
	resp, st := t.inner.CreateAuthProvider(ctx, orgId, authProvider)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListAuthProviders(ctx context.Context, orgId uuid.UUID, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {
	ctx, span := startSpan(ctx, "ListAuthProviders")
	resp, st := t.inner.ListAuthProviders(ctx, orgId, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ListAllAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {
	ctx, span := startSpan(ctx, "ListAllAuthProviders")
	resp, st := t.inner.ListAllAuthProviders(ctx, params)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "GetAuthProvider")
	resp, st := t.inner.GetAuthProvider(ctx, orgId, name)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "GetAuthProviderByIssuerAndClientId")
	resp, st := t.inner.GetAuthProviderByIssuerAndClientId(ctx, orgId, issuer, clientId)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "GetAuthProviderByAuthorizationUrl")
	resp, st := t.inner.GetAuthProviderByAuthorizationUrl(ctx, orgId, authorizationUrl)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "ReplaceAuthProvider")
	resp, st := t.inner.ReplaceAuthProvider(ctx, orgId, name, authProvider)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.AuthProvider, api.Status) {
	ctx, span := startSpan(ctx, "PatchAuthProvider")
	resp, st := t.inner.PatchAuthProvider(ctx, orgId, name, patch)
	endSpan(span, st)
	return resp, st
}

func (t *TracedService) DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) api.Status {
	ctx, span := startSpan(ctx, "DeleteAuthProvider")
	st := t.inner.DeleteAuthProvider(ctx, orgId, name)
	endSpan(span, st)
	return st
}

// --- Auth ---
func (t *TracedService) GetAuthConfig(ctx context.Context, authConfig *api.AuthConfig) (*api.AuthConfig, api.Status) {
	ctx, span := startSpan(ctx, "GetAuthConfig")
	resp, st := t.inner.GetAuthConfig(ctx, authConfig)
	endSpan(span, st)
	return resp, st
}
