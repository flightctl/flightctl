// Code generated for wrapping service methods with OpenTelemetry tracing

package service

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/store/selector"
	"go.opentelemetry.io/otel/trace"
)

type TracedService struct {
	inner Service
}

func WrapWithTracing(svc Service) Service {
	return &TracedService{inner: svc}
}

func start(ctx context.Context, method string) (context.Context, func(options ...trace.SpanEndOption)) {
	ctx, span := instrumentation.StartSpan(ctx, "flightctl/service", method)
	return ctx, span.End
}

// --- CertificateSigningRequest ---
func (t *TracedService) DeleteCertificateSigningRequests(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteCertificateSigningRequests")
	defer done()
	return t.inner.DeleteCertificateSigningRequests(ctx)
}
func (t *TracedService) ListCertificateSigningRequests(ctx context.Context, p api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status) {
	ctx, done := start(ctx, "ListCertificateSigningRequests")
	defer done()
	return t.inner.ListCertificateSigningRequests(ctx, p)
}
func (t *TracedService) CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, done := start(ctx, "CreateCertificateSigningRequest")
	defer done()
	return t.inner.CreateCertificateSigningRequest(ctx, csr)
}
func (t *TracedService) DeleteCertificateSigningRequest(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteCertificateSigningRequest")
	defer done()
	return t.inner.DeleteCertificateSigningRequest(ctx, name)
}
func (t *TracedService) GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, api.Status) {
	ctx, done := start(ctx, "GetCertificateSigningRequest")
	defer done()
	return t.inner.GetCertificateSigningRequest(ctx, name)
}
func (t *TracedService) PatchCertificateSigningRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, done := start(ctx, "PatchCertificateSigningRequest")
	defer done()
	return t.inner.PatchCertificateSigningRequest(ctx, name, patch)
}
func (t *TracedService) ReplaceCertificateSigningRequest(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status) {
	ctx, done := start(ctx, "ReplaceCertificateSigningRequest")
	defer done()
	return t.inner.ReplaceCertificateSigningRequest(ctx, name, csr)
}

// --- Device ---
func (t *TracedService) CreateDevice(ctx context.Context, d api.Device) (*api.Device, api.Status) {
	ctx, done := start(ctx, "CreateDevice")
	defer done()
	return t.inner.CreateDevice(ctx, d)
}
func (t *TracedService) ListDevices(ctx context.Context, p api.ListDevicesParams, sel *selector.AnnotationSelector) (*api.DeviceList, api.Status) {
	ctx, done := start(ctx, "ListDevices")
	defer done()
	return t.inner.ListDevices(ctx, p, sel)
}
func (t *TracedService) UpdateDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, error) {
	ctx, done := start(ctx, "UpdateDevice")
	defer done()
	return t.inner.UpdateDevice(ctx, name, device, fieldsToUnset)
}
func (t *TracedService) DeleteDevices(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteDevices")
	defer done()
	return t.inner.DeleteDevices(ctx)
}
func (t *TracedService) GetDevice(ctx context.Context, name string) (*api.Device, api.Status) {
	ctx, done := start(ctx, "GetDevice")
	defer done()
	return t.inner.GetDevice(ctx, name)
}
func (t *TracedService) ReplaceDevice(ctx context.Context, name string, device api.Device, unset []string) (*api.Device, api.Status) {
	ctx, done := start(ctx, "ReplaceDevice")
	defer done()
	return t.inner.ReplaceDevice(ctx, name, device, unset)
}
func (t *TracedService) DeleteDevice(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteDevice")
	defer done()
	return t.inner.DeleteDevice(ctx, name)
}
func (t *TracedService) GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status) {
	ctx, done := start(ctx, "GetDeviceStatus")
	defer done()
	return t.inner.GetDeviceStatus(ctx, name)
}
func (t *TracedService) ReplaceDeviceStatus(ctx context.Context, name string, device api.Device) (*api.Device, api.Status) {
	ctx, done := start(ctx, "ReplaceDeviceStatus")
	defer done()
	return t.inner.ReplaceDeviceStatus(ctx, name, device)
}
func (t *TracedService) GetRenderedDevice(ctx context.Context, name string, p api.GetRenderedDeviceParams) (*api.Device, api.Status) {
	ctx, done := start(ctx, "GetRenderedDevice")
	defer done()
	return t.inner.GetRenderedDevice(ctx, name, p)
}
func (t *TracedService) PatchDevice(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status) {
	ctx, done := start(ctx, "PatchDevice")
	defer done()
	return t.inner.PatchDevice(ctx, name, patch)
}
func (t *TracedService) DecommissionDevice(ctx context.Context, name string, decom api.DeviceDecommission) (*api.Device, api.Status) {
	ctx, done := start(ctx, "DecommissionDevice")
	defer done()
	return t.inner.DecommissionDevice(ctx, name, decom)
}
func (t *TracedService) UpdateDeviceAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	ctx, done := start(ctx, "UpdateDeviceAnnotations")
	defer done()
	return t.inner.UpdateDeviceAnnotations(ctx, name, annotations, deleteKeys)
}
func (t *TracedService) UpdateRenderedDevice(ctx context.Context, name, renderedConfig, renderedApps string) api.Status {
	ctx, done := start(ctx, "UpdateRenderedDevice")
	defer done()
	return t.inner.UpdateRenderedDevice(ctx, name, renderedConfig, renderedApps)
}
func (t *TracedService) SetDeviceServiceConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	ctx, done := start(ctx, "SetDeviceServiceConditions")
	defer done()
	return t.inner.SetDeviceServiceConditions(ctx, name, conditions)
}
func (t *TracedService) OverwriteDeviceRepositoryRefs(ctx context.Context, name string, refs ...string) api.Status {
	ctx, done := start(ctx, "OverwriteDeviceRepositoryRefs")
	defer done()
	return t.inner.OverwriteDeviceRepositoryRefs(ctx, name, refs...)
}
func (t *TracedService) GetDeviceRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	ctx, done := start(ctx, "GetDeviceRepositoryRefs")
	defer done()
	return t.inner.GetDeviceRepositoryRefs(ctx, name)
}
func (t *TracedService) CountDevices(ctx context.Context, p api.ListDevicesParams, sel *selector.AnnotationSelector) (int64, api.Status) {
	ctx, done := start(ctx, "CountDevices")
	defer done()
	return t.inner.CountDevices(ctx, p, sel)
}
func (t *TracedService) UnmarkDevicesRolloutSelection(ctx context.Context, fleetName string) api.Status {
	ctx, done := start(ctx, "UnmarkDevicesRolloutSelection")
	defer done()
	return t.inner.UnmarkDevicesRolloutSelection(ctx, fleetName)
}
func (t *TracedService) MarkDevicesRolloutSelection(ctx context.Context, p api.ListDevicesParams, sel *selector.AnnotationSelector, limit *int) api.Status {
	ctx, done := start(ctx, "MarkDevicesRolloutSelection")
	defer done()
	return t.inner.MarkDevicesRolloutSelection(ctx, p, sel, limit)
}
func (t *TracedService) GetDeviceCompletionCounts(ctx context.Context, owner, version string, timeout *time.Duration) ([]api.DeviceCompletionCount, api.Status) {
	ctx, done := start(ctx, "GetDeviceCompletionCounts")
	defer done()
	return t.inner.GetDeviceCompletionCounts(ctx, owner, version, timeout)
}
func (t *TracedService) CountDevicesByLabels(ctx context.Context, p api.ListDevicesParams, sel *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status) {
	ctx, done := start(ctx, "CountDevicesByLabels")
	defer done()
	return t.inner.CountDevicesByLabels(ctx, p, sel, groupBy)
}
func (t *TracedService) GetDevicesSummary(ctx context.Context, p api.ListDevicesParams, sel *selector.AnnotationSelector) (*api.DevicesSummary, api.Status) {
	ctx, done := start(ctx, "GetDevicesSummary")
	defer done()
	return t.inner.GetDevicesSummary(ctx, p, sel)
}
func (t *TracedService) UpdateDeviceSummaryStatusBatch(ctx context.Context, names []string, status api.DeviceSummaryStatusType, info string) api.Status {
	ctx, done := start(ctx, "UpdateDeviceSummaryStatusBatch")
	defer done()
	return t.inner.UpdateDeviceSummaryStatusBatch(ctx, names, status, info)
}
func (t *TracedService) UpdateServiceSideDeviceStatus(ctx context.Context, device api.Device) bool {
	ctx, done := start(ctx, "UpdateServiceSideDeviceStatus")
	defer done()
	return t.inner.UpdateServiceSideDeviceStatus(ctx, device)
}

// --- EnrollmentConfig ---
func (t *TracedService) GetEnrollmentConfig(ctx context.Context, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status) {
	ctx, done := start(ctx, "GetEnrollmentConfig")
	defer done()
	return t.inner.GetEnrollmentConfig(ctx, params)
}

// --- EnrollmentRequest ---
func (t *TracedService) CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "CreateEnrollmentRequest")
	defer done()
	return t.inner.CreateEnrollmentRequest(ctx, er)
}
func (t *TracedService) ListEnrollmentRequests(ctx context.Context, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status) {
	ctx, done := start(ctx, "ListEnrollmentRequests")
	defer done()
	return t.inner.ListEnrollmentRequests(ctx, params)
}
func (t *TracedService) DeleteEnrollmentRequests(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteEnrollmentRequests")
	defer done()
	return t.inner.DeleteEnrollmentRequests(ctx)
}
func (t *TracedService) GetEnrollmentRequest(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "GetEnrollmentRequest")
	defer done()
	return t.inner.GetEnrollmentRequest(ctx, name)
}
func (t *TracedService) ReplaceEnrollmentRequest(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "ReplaceEnrollmentRequest")
	defer done()
	return t.inner.ReplaceEnrollmentRequest(ctx, name, er)
}
func (t *TracedService) PatchEnrollmentRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "PatchEnrollmentRequest")
	defer done()
	return t.inner.PatchEnrollmentRequest(ctx, name, patch)
}
func (t *TracedService) DeleteEnrollmentRequest(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteEnrollmentRequest")
	defer done()
	return t.inner.DeleteEnrollmentRequest(ctx, name)
}
func (t *TracedService) GetEnrollmentRequestStatus(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "GetEnrollmentRequestStatus")
	defer done()
	return t.inner.GetEnrollmentRequestStatus(ctx, name)
}
func (t *TracedService) ApproveEnrollmentRequest(ctx context.Context, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status) {
	ctx, done := start(ctx, "ApproveEnrollmentRequest")
	defer done()
	return t.inner.ApproveEnrollmentRequest(ctx, name, approval)
}
func (t *TracedService) ReplaceEnrollmentRequestStatus(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status) {
	ctx, done := start(ctx, "ReplaceEnrollmentRequestStatus")
	defer done()
	return t.inner.ReplaceEnrollmentRequestStatus(ctx, name, er)
}

// --- Fleet ---
func (t *TracedService) CreateFleet(ctx context.Context, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "CreateFleet")
	defer done()
	return t.inner.CreateFleet(ctx, fleet)
}
func (t *TracedService) ListFleets(ctx context.Context, params api.ListFleetsParams) (*api.FleetList, api.Status) {
	ctx, done := start(ctx, "ListFleets")
	defer done()
	return t.inner.ListFleets(ctx, params)
}
func (t *TracedService) DeleteFleets(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteFleets")
	defer done()
	return t.inner.DeleteFleets(ctx)
}
func (t *TracedService) GetFleet(ctx context.Context, name string, params api.GetFleetParams) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "GetFleet")
	defer done()
	return t.inner.GetFleet(ctx, name, params)
}
func (t *TracedService) ReplaceFleet(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "ReplaceFleet")
	defer done()
	return t.inner.ReplaceFleet(ctx, name, fleet)
}
func (t *TracedService) DeleteFleet(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteFleet")
	defer done()
	return t.inner.DeleteFleet(ctx, name)
}
func (t *TracedService) GetFleetStatus(ctx context.Context, name string) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "GetFleetStatus")
	defer done()
	return t.inner.GetFleetStatus(ctx, name)
}
func (t *TracedService) ReplaceFleetStatus(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "ReplaceFleetStatus")
	defer done()
	return t.inner.ReplaceFleetStatus(ctx, name, fleet)
}
func (t *TracedService) PatchFleet(ctx context.Context, name string, patch api.PatchRequest) (*api.Fleet, api.Status) {
	ctx, done := start(ctx, "PatchFleet")
	defer done()
	return t.inner.PatchFleet(ctx, name, patch)
}
func (t *TracedService) ListFleetRolloutDeviceSelection(ctx context.Context) (*api.FleetList, api.Status) {
	ctx, done := start(ctx, "ListFleetRolloutDeviceSelection")
	defer done()
	return t.inner.ListFleetRolloutDeviceSelection(ctx)
}
func (t *TracedService) ListDisruptionBudgetFleets(ctx context.Context) (*api.FleetList, api.Status) {
	ctx, done := start(ctx, "ListDisruptionBudgetFleets")
	defer done()
	return t.inner.ListDisruptionBudgetFleets(ctx)
}
func (t *TracedService) UpdateFleetConditions(ctx context.Context, name string, conditions []api.Condition) api.Status {
	ctx, done := start(ctx, "UpdateFleetConditions")
	defer done()
	return t.inner.UpdateFleetConditions(ctx, name, conditions)
}
func (t *TracedService) UpdateFleetAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status {
	ctx, done := start(ctx, "UpdateFleetAnnotations")
	defer done()
	return t.inner.UpdateFleetAnnotations(ctx, name, annotations, deleteKeys)
}
func (t *TracedService) OverwriteFleetRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status {
	ctx, done := start(ctx, "OverwriteFleetRepositoryRefs")
	defer done()
	return t.inner.OverwriteFleetRepositoryRefs(ctx, name, repositoryNames...)
}
func (t *TracedService) GetFleetRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status) {
	ctx, done := start(ctx, "GetFleetRepositoryRefs")
	defer done()
	return t.inner.GetFleetRepositoryRefs(ctx, name)
}

// Additional components (Labels, Repository, ResourceSync, TemplateVersion) to be appended next.

// --- Labels ---
func (t *TracedService) ListLabels(ctx context.Context, params api.ListLabelsParams) (*api.LabelList, api.Status) {
	ctx, done := start(ctx, "ListLabels")
	defer done()
	return t.inner.ListLabels(ctx, params)
}

// --- Repository ---
func (t *TracedService) CreateRepository(ctx context.Context, repo api.Repository) (*api.Repository, api.Status) {
	ctx, done := start(ctx, "CreateRepository")
	defer done()
	return t.inner.CreateRepository(ctx, repo)
}
func (t *TracedService) ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status) {
	ctx, done := start(ctx, "ListRepositories")
	defer done()
	return t.inner.ListRepositories(ctx, params)
}
func (t *TracedService) DeleteRepositories(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteRepositories")
	defer done()
	return t.inner.DeleteRepositories(ctx)
}
func (t *TracedService) GetRepository(ctx context.Context, name string) (*api.Repository, api.Status) {
	ctx, done := start(ctx, "GetRepository")
	defer done()
	return t.inner.GetRepository(ctx, name)
}
func (t *TracedService) ReplaceRepository(ctx context.Context, name string, repo api.Repository) (*api.Repository, api.Status) {
	ctx, done := start(ctx, "ReplaceRepository")
	defer done()
	return t.inner.ReplaceRepository(ctx, name, repo)
}
func (t *TracedService) DeleteRepository(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteRepository")
	defer done()
	return t.inner.DeleteRepository(ctx, name)
}
func (t *TracedService) PatchRepository(ctx context.Context, name string, patch api.PatchRequest) (*api.Repository, api.Status) {
	ctx, done := start(ctx, "PatchRepository")
	defer done()
	return t.inner.PatchRepository(ctx, name, patch)
}
func (t *TracedService) ReplaceRepositoryStatus(ctx context.Context, name string, repository api.Repository) (*api.Repository, api.Status) {
	ctx, done := start(ctx, "ReplaceRepositoryStatus")
	defer done()
	return t.inner.ReplaceRepositoryStatus(ctx, name, repository)
}
func (t *TracedService) GetRepositoryFleetReferences(ctx context.Context, name string) (*api.FleetList, api.Status) {
	ctx, done := start(ctx, "GetRepositoryFleetReferences")
	defer done()
	return t.inner.GetRepositoryFleetReferences(ctx, name)
}
func (t *TracedService) GetRepositoryDeviceReferences(ctx context.Context, name string) (*api.DeviceList, api.Status) {
	ctx, done := start(ctx, "GetRepositoryDeviceReferences")
	defer done()
	return t.inner.GetRepositoryDeviceReferences(ctx, name)
}

// --- ResourceSync ---
func (t *TracedService) CreateResourceSync(ctx context.Context, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, done := start(ctx, "CreateResourceSync")
	defer done()
	return t.inner.CreateResourceSync(ctx, rs)
}
func (t *TracedService) ListResourceSyncs(ctx context.Context, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status) {
	ctx, done := start(ctx, "ListResourceSyncs")
	defer done()
	return t.inner.ListResourceSyncs(ctx, params)
}
func (t *TracedService) DeleteResourceSyncs(ctx context.Context) api.Status {
	ctx, done := start(ctx, "DeleteResourceSyncs")
	defer done()
	return t.inner.DeleteResourceSyncs(ctx)
}
func (t *TracedService) GetResourceSync(ctx context.Context, name string) (*api.ResourceSync, api.Status) {
	ctx, done := start(ctx, "GetResourceSync")
	defer done()
	return t.inner.GetResourceSync(ctx, name)
}
func (t *TracedService) ReplaceResourceSync(ctx context.Context, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, done := start(ctx, "ReplaceResourceSync")
	defer done()
	return t.inner.ReplaceResourceSync(ctx, name, rs)
}
func (t *TracedService) DeleteResourceSync(ctx context.Context, name string) api.Status {
	ctx, done := start(ctx, "DeleteResourceSync")
	defer done()
	return t.inner.DeleteResourceSync(ctx, name)
}
func (t *TracedService) PatchResourceSync(ctx context.Context, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status) {
	ctx, done := start(ctx, "PatchResourceSync")
	defer done()
	return t.inner.PatchResourceSync(ctx, name, patch)
}
func (t *TracedService) ReplaceResourceSyncStatus(ctx context.Context, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status) {
	ctx, done := start(ctx, "ReplaceResourceSyncStatus")
	defer done()
	return t.inner.ReplaceResourceSyncStatus(ctx, name, resourceSync)
}

// --- TemplateVersion ---
func (t *TracedService) CreateTemplateVersion(ctx context.Context, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status) {
	ctx, done := start(ctx, "CreateTemplateVersion")
	defer done()
	return t.inner.CreateTemplateVersion(ctx, tv, immediateRollout)
}
func (t *TracedService) ListTemplateVersions(ctx context.Context, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status) {
	ctx, done := start(ctx, "ListTemplateVersions")
	defer done()
	return t.inner.ListTemplateVersions(ctx, fleet, params)
}
func (t *TracedService) DeleteTemplateVersions(ctx context.Context, fleet string) api.Status {
	ctx, done := start(ctx, "DeleteTemplateVersions")
	defer done()
	return t.inner.DeleteTemplateVersions(ctx, fleet)
}
func (t *TracedService) GetTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status) {
	ctx, done := start(ctx, "GetTemplateVersion")
	defer done()
	return t.inner.GetTemplateVersion(ctx, fleet, name)
}
func (t *TracedService) DeleteTemplateVersion(ctx context.Context, fleet string, name string) api.Status {
	ctx, done := start(ctx, "DeleteTemplateVersion")
	defer done()
	return t.inner.DeleteTemplateVersion(ctx, fleet, name)
}
func (t *TracedService) GetLatestTemplateVersion(ctx context.Context, fleet string) (*api.TemplateVersion, api.Status) {
	ctx, done := start(ctx, "DeleteTemplateVersion")
	defer done()
	return t.inner.GetLatestTemplateVersion(ctx, fleet)
}
