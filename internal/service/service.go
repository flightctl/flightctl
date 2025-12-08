package service

import (
	"context"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

type Service interface {
	// CertificateSigningRequest
	ListCertificateSigningRequests(ctx context.Context, orgId uuid.UUID, params api.ListCertificateSigningRequestsParams) (*api.CertificateSigningRequestList, api.Status)
	CreateCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)
	DeleteCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) api.Status
	GetCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.CertificateSigningRequest, api.Status)
	PatchCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.CertificateSigningRequest, api.Status)
	ReplaceCertificateSigningRequest(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)
	UpdateCertificateSigningRequestApproval(ctx context.Context, orgId uuid.UUID, name string, csr api.CertificateSigningRequest) (*api.CertificateSigningRequest, api.Status)

	// Device
	CreateDevice(ctx context.Context, orgId uuid.UUID, device api.Device) (*api.Device, api.Status)
	ListDevices(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DeviceList, api.Status)
	ListDevicesByServiceCondition(ctx context.Context, orgId uuid.UUID, conditionType string, conditionStatus string, listParams store.ListParams) (*api.DeviceList, api.Status)
	UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device api.Device, fieldsToUnset []string) (*api.Device, error)
	GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, api.Status)
	ReplaceDevice(ctx context.Context, orgId uuid.UUID, name string, device api.Device, fieldsToUnset []string) (*api.Device, api.Status)
	DeleteDevice(ctx context.Context, orgId uuid.UUID, name string) api.Status
	GetDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.Device, api.Status)
	GetDeviceLastSeen(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceLastSeen, api.Status)
	ReplaceDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, device api.Device) (*api.Device, api.Status)
	PatchDeviceStatus(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Device, api.Status)
	GetRenderedDevice(ctx context.Context, orgId uuid.UUID, name string, params api.GetRenderedDeviceParams) (*api.Device, api.Status)
	PatchDevice(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Device, api.Status)
	DecommissionDevice(ctx context.Context, orgId uuid.UUID, name string, decom api.DeviceDecommission) (*api.Device, api.Status)

	ResumeDevices(ctx context.Context, orgId uuid.UUID, request api.DeviceResumeRequest) (api.DeviceResumeResponse, api.Status)
	UpdateDeviceAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) api.Status
	UpdateRenderedDevice(ctx context.Context, orgId uuid.UUID, name, renderedConfig, renderedApplications, specHash string) api.Status
	SetDeviceServiceConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) api.Status
	OverwriteDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) api.Status
	GetDeviceRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, api.Status)
	CountDevices(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (int64, api.Status)
	UnmarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, fleetName string) api.Status
	MarkDevicesRolloutSelection(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, limit *int) api.Status
	GetDeviceCompletionCounts(ctx context.Context, orgId uuid.UUID, owner string, templateVersion string, updateTimeout *time.Duration) ([]api.DeviceCompletionCount, api.Status)
	CountDevicesByLabels(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector, groupBy []string) ([]map[string]any, api.Status)
	GetDevicesSummary(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, annotationSelector *selector.AnnotationSelector) (*api.DevicesSummary, api.Status)
	UpdateServiceSideDeviceStatus(ctx context.Context, orgId uuid.UUID, device api.Device) bool
	SetOutOfDate(ctx context.Context, orgId uuid.UUID, owner string) error
	UpdateServerSideDeviceStatus(ctx context.Context, orgId uuid.UUID, name string) error
	ListDisconnectedDevices(ctx context.Context, orgId uuid.UUID, params api.ListDevicesParams, cutoffTime time.Time) (*api.DeviceList, api.Status)

	// EnrollmentConfig
	GetEnrollmentConfig(ctx context.Context, orgId uuid.UUID, params api.GetEnrollmentConfigParams) (*api.EnrollmentConfig, api.Status)

	//EnrollmentRequest
	CreateEnrollmentRequest(ctx context.Context, orgId uuid.UUID, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)
	ListEnrollmentRequests(ctx context.Context, orgId uuid.UUID, params api.ListEnrollmentRequestsParams) (*api.EnrollmentRequestList, api.Status)
	GetEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, api.Status)
	ReplaceEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)
	PatchEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.EnrollmentRequest, api.Status)
	DeleteEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string) api.Status
	GetEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.EnrollmentRequest, api.Status)
	ApproveEnrollmentRequest(ctx context.Context, orgId uuid.UUID, name string, approval api.EnrollmentRequestApproval) (*api.EnrollmentRequestApprovalStatus, api.Status)
	ReplaceEnrollmentRequestStatus(ctx context.Context, orgId uuid.UUID, name string, er api.EnrollmentRequest) (*api.EnrollmentRequest, api.Status)

	// Fleet
	CreateFleet(ctx context.Context, orgId uuid.UUID, fleet api.Fleet) (*api.Fleet, api.Status)
	ListFleets(ctx context.Context, orgId uuid.UUID, params api.ListFleetsParams) (*api.FleetList, api.Status)
	GetFleet(ctx context.Context, orgId uuid.UUID, name string, params api.GetFleetParams) (*api.Fleet, api.Status)
	ReplaceFleet(ctx context.Context, orgId uuid.UUID, name string, fleet api.Fleet) (*api.Fleet, api.Status)
	DeleteFleet(ctx context.Context, orgId uuid.UUID, name string) api.Status
	GetFleetStatus(ctx context.Context, orgId uuid.UUID, name string) (*api.Fleet, api.Status)
	ReplaceFleetStatus(ctx context.Context, orgId uuid.UUID, name string, fleet api.Fleet) (*api.Fleet, api.Status)
	PatchFleet(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Fleet, api.Status)
	ListFleetRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*api.FleetList, api.Status)
	ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*api.FleetList, api.Status)
	UpdateFleetConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) api.Status
	UpdateFleetAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) api.Status
	OverwriteFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) api.Status
	GetFleetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, api.Status)

	// Labels
	ListLabels(ctx context.Context, orgId uuid.UUID, params api.ListLabelsParams) (*api.LabelList, api.Status)

	// Repository
	CreateRepository(ctx context.Context, orgId uuid.UUID, repo api.Repository) (*api.Repository, api.Status)
	ListRepositories(ctx context.Context, orgId uuid.UUID, params api.ListRepositoriesParams) (*api.RepositoryList, api.Status)
	GetRepository(ctx context.Context, orgId uuid.UUID, name string) (*api.Repository, api.Status)
	ReplaceRepository(ctx context.Context, orgId uuid.UUID, name string, repo api.Repository) (*api.Repository, api.Status)
	DeleteRepository(ctx context.Context, orgId uuid.UUID, name string) api.Status
	PatchRepository(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.Repository, api.Status)
	ReplaceRepositoryStatusByError(ctx context.Context, orgId uuid.UUID, name string, repository api.Repository, err error) (*api.Repository, api.Status)
	GetRepositoryFleetReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.FleetList, api.Status)
	GetRepositoryDeviceReferences(ctx context.Context, orgId uuid.UUID, name string) (*api.DeviceList, api.Status)

	// AuthProvider
	CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider api.AuthProvider) (*api.AuthProvider, api.Status)
	ListAuthProviders(ctx context.Context, orgId uuid.UUID, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status)
	ListAllAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status)
	GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, api.Status)
	GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, api.Status)
	GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*api.AuthProvider, api.Status)
	ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider api.AuthProvider) (*api.AuthProvider, api.Status)
	PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.AuthProvider, api.Status)
	DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) api.Status

	// Auth
	GetAuthConfig(ctx context.Context, authConfig *api.AuthConfig) (*api.AuthConfig, api.Status)

	// ResourceSync
	CreateResourceSync(ctx context.Context, orgId uuid.UUID, rs api.ResourceSync) (*api.ResourceSync, api.Status)
	ListResourceSyncs(ctx context.Context, orgId uuid.UUID, params api.ListResourceSyncsParams) (*api.ResourceSyncList, api.Status)
	GetResourceSync(ctx context.Context, orgId uuid.UUID, name string) (*api.ResourceSync, api.Status)
	ReplaceResourceSync(ctx context.Context, orgId uuid.UUID, name string, rs api.ResourceSync) (*api.ResourceSync, api.Status)
	DeleteResourceSync(ctx context.Context, orgId uuid.UUID, name string) api.Status
	PatchResourceSync(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.ResourceSync, api.Status)
	ReplaceResourceSyncStatus(ctx context.Context, orgId uuid.UUID, name string, resourceSync api.ResourceSync) (*api.ResourceSync, api.Status)

	// TemplateVersion
	CreateTemplateVersion(ctx context.Context, orgId uuid.UUID, tv api.TemplateVersion, immediateRollout bool) (*api.TemplateVersion, api.Status)
	ListTemplateVersions(ctx context.Context, orgId uuid.UUID, fleet string, params api.ListTemplateVersionsParams) (*api.TemplateVersionList, api.Status)
	GetTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, api.Status)
	DeleteTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string, name string) api.Status
	GetLatestTemplateVersion(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, api.Status)

	// Event
	CreateEvent(ctx context.Context, orgId uuid.UUID, event *api.Event)
	ListEvents(ctx context.Context, orgId uuid.UUID, params api.ListEventsParams) (*api.EventList, api.Status)
	DeleteEventsOlderThan(ctx context.Context, cutoffTime time.Time) (int64, api.Status)

	// Checkpoint
	GetCheckpoint(ctx context.Context, consumer string, key string) ([]byte, api.Status)
	SetCheckpoint(ctx context.Context, consumer string, key string, value []byte) api.Status
	GetDatabaseTime(ctx context.Context) (time.Time, api.Status)

	// Organization
	ListOrganizations(ctx context.Context, params api.ListOrganizationsParams) (*api.OrganizationList, api.Status)
}
