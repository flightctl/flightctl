package service

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
)

type Service interface {
	// CertificateSigningRequest
	ListCertificateSigningRequests(ctx context.Context, params api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status)
	CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)
	DeleteCertificateSigningRequest(ctx context.Context, name string) api.Status
	GetCertificateSigningRequest(ctx context.Context, name string) (*api.CertificateSigningRequest, api.Status)
	PatchCertificateSigningRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status)
	ReplaceCertificateSigningRequest(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)
	UpdateCertificateSigningRequestApproval(ctx context.Context, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)

	// Device
	CreateDevice(ctx context.Context, device api.Device) (*api.Device, api.Status)
	ListDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DeviceList, api.Status)
	ListDevicesByServiceCondition(ctx context.Context, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, api.Status)
	UpdateDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, error)
	GetDevice(ctx context.Context, name string) (*api.Device, api.Status)
	ReplaceDevice(ctx context.Context, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status)
	DeleteDevice(ctx context.Context, name string) api.Status
	GetDeviceStatus(ctx context.Context, name string) (*api.Device, api.Status)
	GetDeviceLastSeen(ctx context.Context, name string) (*api.DeviceLastSeen, api.Status)
	ReplaceDeviceStatus(ctx context.Context, name string, device api.Device) (*api.Device, api.Status)
	PatchDeviceStatus(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status)
	GetRenderedDevice(ctx context.Context, name string, params api.GetRenderedDeviceParams) (*api.Device, api.Status)
	PatchDevice(ctx context.Context, name string, patch api.PatchRequest) (*api.Device, api.Status)
	DecommissionDevice(ctx context.Context, name string, decom api.DeviceDecommission) (*api.Device, api.Status)

	ResumeDevices(ctx context.Context, request api.DeviceResumeRequest) (api.DeviceResumeResponse, api.Status)
	UpdateDeviceAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status
	UpdateRenderedDevice(ctx context.Context, name, renderedConfig, renderedApplications, specHash string) api.Status
	SetDeviceServiceConditions(ctx context.Context, name string, conditions []api.Condition) api.Status
	OverwriteDeviceRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status
	GetDeviceRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status)
	CountDevices(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, api.Status)
	UnmarkDevicesRolloutSelection(ctx context.Context, fleetName string) api.Status
	MarkDevicesRolloutSelection(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) api.Status
	GetDeviceCompletionCounts(ctx context.Context, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, api.Status)
	CountDevicesByLabels(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status)
	GetDevicesSummary(ctx context.Context, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DevicesSummary, api.Status)
	UpdateServiceSideDeviceStatus(ctx context.Context, device api.Device) bool
	SetOutOfDate(ctx context.Context, owner string) error
	UpdateServerSideDeviceStatus(ctx context.Context, name string) error

	// EnrollmentConfig
	GetEnrollmentConfig(ctx context.Context, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status)

	//EnrollmentRequest
	CreateEnrollmentRequest(ctx context.Context, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)
	ListEnrollmentRequests(ctx context.Context, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status)
	GetEnrollmentRequest(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status)
	ReplaceEnrollmentRequest(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)
	PatchEnrollmentRequest(ctx context.Context, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status)
	DeleteEnrollmentRequest(ctx context.Context, name string) api.Status
	GetEnrollmentRequestStatus(ctx context.Context, name string) (*api.EnrollmentRequest, api.Status)
	ApproveEnrollmentRequest(ctx context.Context, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status)
	ReplaceEnrollmentRequestStatus(ctx context.Context, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)

	// Fleet
	CreateFleet(ctx context.Context, fleet api.Fleet) (*api.Fleet, api.Status)
	ListFleets(ctx context.Context, params api.ListFleetsParams) (*api.FleetList, api.Status)
	GetFleet(ctx context.Context, name string, params api.GetFleetParams) (*api.Fleet, api.Status)
	ReplaceFleet(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status)
	DeleteFleet(ctx context.Context, name string) api.Status
	GetFleetStatus(ctx context.Context, name string) (*api.Fleet, api.Status)
	ReplaceFleetStatus(ctx context.Context, name string, fleet api.Fleet) (*api.Fleet, api.Status)
	PatchFleet(ctx context.Context, name string, patch api.PatchRequest) (*api.Fleet, api.Status)
	ListFleetRolloutDeviceSelection(ctx context.Context) (*api.FleetList, api.Status)
	ListDisruptionBudgetFleets(ctx context.Context) (*api.FleetList, api.Status)
	UpdateFleetConditions(ctx context.Context, name string, conditions []api.Condition) api.Status
	UpdateFleetAnnotations(ctx context.Context, name string, annotations map[string]string, deleteKeys []string) api.Status
	OverwriteFleetRepositoryRefs(ctx context.Context, name string, repositoryNames ...string) api.Status
	GetFleetRepositoryRefs(ctx context.Context, name string) (*api.RepositoryList, api.Status)

	// Labels
	ListLabels(ctx context.Context, params api.ListLabelsParams) (*api.LabelList, api.Status)

	// Repository
	CreateRepository(ctx context.Context, repo api.Repository) (*api.Repository, api.Status)
	ListRepositories(ctx context.Context, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status)
	GetRepository(ctx context.Context, name string) (*api.Repository, api.Status)
	ReplaceRepository(ctx context.Context, name string, repo api.Repository) (*api.Repository, api.Status)
	DeleteRepository(ctx context.Context, name string) api.Status
	PatchRepository(ctx context.Context, name string, patch api.PatchRequest) (*api.Repository, api.Status)
	ReplaceRepositoryStatusByError(ctx context.Context, name string, repository api.Repository, err error) (*api.Repository, api.Status)
	GetRepositoryFleetReferences(ctx context.Context, name string) (*api.FleetList, api.Status)
	GetRepositoryDeviceReferences(ctx context.Context, name string) (*api.DeviceList, api.Status)

	// ResourceSync
	CreateResourceSync(ctx context.Context, rs api.ResourceSync) (*api.ResourceSync, api.Status)
	ListResourceSyncs(ctx context.Context, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status)
	GetResourceSync(ctx context.Context, name string) (*api.ResourceSync, api.Status)
	ReplaceResourceSync(ctx context.Context, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status)
	DeleteResourceSync(ctx context.Context, name string) api.Status
	PatchResourceSync(ctx context.Context, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status)
	ReplaceResourceSyncStatus(ctx context.Context, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status)

	// TemplateVersion
	CreateTemplateVersion(ctx context.Context, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status)
	ListTemplateVersions(ctx context.Context, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status)
	GetTemplateVersion(ctx context.Context, fleet string, name string) (*api.TemplateVersion, api.Status)
	DeleteTemplateVersion(ctx context.Context, fleet string, name string) api.Status
	GetLatestTemplateVersion(ctx context.Context, fleet string) (*api.TemplateVersion, api.Status)

	// Event
	CreateEvent(ctx context.Context, event *api.Event)
	ListEvents(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status)
	DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status)

	// Checkpoint
	GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, api.Status)
	SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) api.Status
	GetDatabaseTime(ctx context.Context) (time.Time, api.Status)

	// Organization
	ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status)
}
