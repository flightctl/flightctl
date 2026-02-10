package domain

import (
	v1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

// ========== API Group ==========

const APIGroup = v1beta1.APIGroup

// ========== CertificateSigningRequest ==========

const (
	CertificateSigningRequestAPIVersion = v1beta1.CertificateSigningRequestAPIVersion
	CertificateSigningRequestKind       = v1beta1.CertificateSigningRequestKind
	CertificateSigningRequestListKind   = v1beta1.CertificateSigningRequestListKind
)

// ========== Device ==========

const (
	DeviceAPIVersion = v1beta1.DeviceAPIVersion
	DeviceKind       = v1beta1.DeviceKind
	DeviceListKind   = v1beta1.DeviceListKind
)

// Device annotation keys
const (
	DeviceAnnotationConsole                 = v1beta1.DeviceAnnotationConsole
	DeviceAnnotationRenderedVersion         = v1beta1.DeviceAnnotationRenderedVersion
	DeviceAnnotationAwaitingReconnect       = v1beta1.DeviceAnnotationAwaitingReconnect
	DeviceAnnotationConflictPaused          = v1beta1.DeviceAnnotationConflictPaused
	DeviceAnnotationTemplateVersion         = v1beta1.DeviceAnnotationTemplateVersion
	DeviceAnnotationRenderedTemplateVersion = v1beta1.DeviceAnnotationRenderedTemplateVersion
	DeviceAnnotationRenderedSpecHash        = v1beta1.DeviceAnnotationRenderedSpecHash
	DeviceAnnotationSelectedForRollout      = v1beta1.DeviceAnnotationSelectedForRollout
	DeviceAnnotationLastRolloutError        = v1beta1.DeviceAnnotationLastRolloutError
)

const DeviceDisconnectedTimeout = v1beta1.DeviceDisconnectedTimeout
const DeviceQueryConsoleSessionMetadata = v1beta1.DeviceQueryConsoleSessionMetadata

// ========== EnrollmentRequest ==========

const (
	EnrollmentRequestAPIVersion = v1beta1.EnrollmentRequestAPIVersion
	EnrollmentRequestKind       = v1beta1.EnrollmentRequestKind
	EnrollmentRequestListKind   = v1beta1.EnrollmentRequestListKind
)

// ========== Fleet ==========

const (
	FleetAPIVersion = v1beta1.FleetAPIVersion
	FleetKind       = v1beta1.FleetKind
	FleetListKind   = v1beta1.FleetListKind
)

// Fleet annotation keys
const (
	FleetAnnotationTemplateVersion             = v1beta1.FleetAnnotationTemplateVersion
	FleetAnnotationDeployingTemplateVersion    = v1beta1.FleetAnnotationDeployingTemplateVersion
	FleetAnnotationBatchNumber                 = v1beta1.FleetAnnotationBatchNumber
	FleetAnnotationRolloutApproved             = v1beta1.FleetAnnotationRolloutApproved
	FleetAnnotationRolloutApprovalMethod       = v1beta1.FleetAnnotationRolloutApprovalMethod
	FleetAnnotationLastBatchCompletionReport   = v1beta1.FleetAnnotationLastBatchCompletionReport
	FleetAnnotationDeviceSelectionConfigDigest = v1beta1.FleetAnnotationDeviceSelectionConfigDigest
)

// ========== Event ==========

const (
	EventAPIVersion                  = v1beta1.EventAPIVersion
	EventKind                        = v1beta1.EventKind
	EventListKind                    = v1beta1.EventListKind
	EventAnnotationRequestID         = v1beta1.EventAnnotationRequestID
	EventAnnotationDelayDeviceRender = v1beta1.EventAnnotationDelayDeviceRender
)

// ========== Repository ==========

const (
	RepositoryAPIVersion = v1beta1.RepositoryAPIVersion
	RepositoryKind       = v1beta1.RepositoryKind
	RepositoryListKind   = v1beta1.RepositoryListKind
)

// ========== AuthProvider ==========

const (
	AuthProviderAPIVersion                    = v1beta1.AuthProviderAPIVersion
	AuthProviderKind                          = v1beta1.AuthProviderKind
	AuthProviderListKind                      = v1beta1.AuthProviderListKind
	AuthProviderAnnotationCreatedBySuperAdmin = v1beta1.AuthProviderAnnotationCreatedBySuperAdmin
)

// ========== AuthConfig ==========

const (
	AuthConfigAPIVersion = v1beta1.AuthConfigAPIVersion
	AuthConfigKind       = v1beta1.AuthConfigKind
)

// ========== ResourceSync ==========

const (
	ResourceSyncAPIVersion = v1beta1.ResourceSyncAPIVersion
	ResourceSyncKind       = v1beta1.ResourceSyncKind
	ResourceSyncListKind   = v1beta1.ResourceSyncListKind
)

// ========== Catalog ==========

const (
	CatalogAPIVersion   = v1alpha1.CatalogAPIVersion
	CatalogKind         = v1alpha1.CatalogKind
	CatalogListKind     = v1alpha1.CatalogListKind
	CatalogItemKind     = v1alpha1.CatalogItemKind
	CatalogItemListKind = v1alpha1.CatalogItemListKind
)

// ========== TemplateVersion ==========

const (
	TemplateVersionAPIVersion = v1beta1.TemplateVersionAPIVersion
	TemplateVersionKind       = v1beta1.TemplateVersionKind
	TemplateVersionListKind   = v1beta1.TemplateVersionListKind
)

// ========== Organization ==========

const (
	OrganizationAPIVersion = v1beta1.OrganizationAPIVersion
	OrganizationKind       = v1beta1.OrganizationKind
	OrganizationListKind   = v1beta1.OrganizationListKind
	OrganizationIDQueryKey = v1beta1.OrganizationIDQueryKey
)

// ========== System ==========

const (
	SystemKind           = v1beta1.SystemKind
	SystemComponentDB    = v1beta1.SystemComponentDB
	SystemComponentQueue = v1beta1.SystemComponentQueue
)

// ========== Roles ==========

// External role names - these come from authentication providers and are mapped to internal roles
const (
	ExternalRoleAdmin     = v1beta1.ExternalRoleAdmin
	ExternalRoleOrgAdmin  = v1beta1.ExternalRoleOrgAdmin
	ExternalRoleOperator  = v1beta1.ExternalRoleOperator
	ExternalRoleViewer    = v1beta1.ExternalRoleViewer
	ExternalRoleInstaller = v1beta1.ExternalRoleInstaller
)

// Internal role constants - used within flightctl for authorization
const (
	RoleAdmin     = v1beta1.RoleAdmin
	RoleOrgAdmin  = v1beta1.RoleOrgAdmin
	RoleOperator  = v1beta1.RoleOperator
	RoleViewer    = v1beta1.RoleViewer
	RoleInstaller = v1beta1.RoleInstaller
)

// ========== Update State ==========

type UpdateState = v1beta1.UpdateState

const (
	UpdateStatePreparing      = v1beta1.UpdateStatePreparing
	UpdateStateReadyToUpdate  = v1beta1.UpdateStateReadyToUpdate
	UpdateStateApplyingUpdate = v1beta1.UpdateStateApplyingUpdate
	UpdateStateRebooting      = v1beta1.UpdateStateRebooting
	UpdateStateUpdated        = v1beta1.UpdateStateUpdated
	UpdateStateCanceled       = v1beta1.UpdateStateCanceled
	UpdateStateError          = v1beta1.UpdateStateError
	UpdateStateRollingBack    = v1beta1.UpdateStateRollingBack
	UpdateStateRetrying       = v1beta1.UpdateStateRetrying
)

// ========== Decommission State ==========

type DecommissionState = v1beta1.DecommissionState

const (
	DecommissionStateStarted  = v1beta1.DecommissionStateStarted
	DecommissionStateComplete = v1beta1.DecommissionStateComplete
	DecommissionStateError    = v1beta1.DecommissionStateError
)

// ========== Rollout Reasons ==========

const (
	RolloutInactiveReason  = v1beta1.RolloutInactiveReason
	RolloutActiveReason    = v1beta1.RolloutActiveReason
	RolloutSuspendedReason = v1beta1.RolloutSuspendedReason
	RolloutWaitingReason   = v1beta1.RolloutWaitingReason
)

// ========== Batch Names ==========

const (
	PreliminaryBatchName   = v1beta1.PreliminaryBatchName
	FinalImplicitBatchName = v1beta1.FinalImplicitBatchName
)

// ========== System Resource Names ==========

const FlightCtlSystemResourceName = v1beta1.FlightCtlSystemResourceName

// ========== TPM Validation Reasons ==========

const (
	TPMVerificationFailedReason = v1beta1.TPMVerificationFailedReason
	TPMChallengeRequiredReason  = v1beta1.TPMChallengeRequiredReason
	TPMChallengeFailedReason    = v1beta1.TPMChallengeFailedReason
	TPMChallengeSucceededReason = v1beta1.TPMChallengeSucceededReason
)

// ========== ResourceSync Reasons ==========

const ResourceSyncNewHashDetectedReason = v1beta1.ResourceSyncNewHashDetectedReason

// ========== Device Text ==========

const (
	DeviceOutOfDateText          = v1beta1.DeviceOutOfDateText
	DeviceOutOfSyncWithFleetText = v1beta1.DeviceOutOfSyncWithFleetText
)

// ========== Condition Reasons ==========

const DeviceConditionBootstrapReason = v1beta1.DeviceConditionBootstrapReason

// ========== Template Functions ==========

var (
	GetGoTemplateFuncMap      = v1beta1.GetGoTemplateFuncMap
	ExecuteGoTemplateOnDevice = v1beta1.ExecuteGoTemplateOnDevice
)
