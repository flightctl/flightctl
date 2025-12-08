package v1beta1

import (
	"time"

	"github.com/google/uuid"
)

var (
	NullOrgId = uuid.MustParse("00000000-0000-0000-0000-000000000000")
)

const (
	APIGroup = "flightctl.io"

	CertificateSigningRequestAPIVersion = "v1beta1"
	CertificateSigningRequestKind       = "CertificateSigningRequest"
	CertificateSigningRequestListKind   = "CertificateSigningRequestList"

	DeviceAPIVersion = "v1beta1"
	DeviceKind       = "Device"
	DeviceListKind   = "DeviceList"

	DeviceAnnotationConsole         = "device-controller/console"
	DeviceAnnotationRenderedVersion = "device-controller/renderedVersion"
	// Used After database restore , all devices will be marked with this annotation
	DeviceAnnotationAwaitingReconnect = "device-controller/awaitingReconnect"
	// After restore when device has a new spec version than what we know,
	DeviceAnnotationConflictPaused = "device-controller/conflictPaused"
	// This annotation is populated after a device was rolled out by the fleet-rollout task
	DeviceAnnotationTemplateVersion = "fleet-controller/templateVersion"
	// This annotation is populated after a device was rendered by the device-render task
	DeviceAnnotationRenderedTemplateVersion = "fleet-controller/renderedTemplateVersion"
	// This annotation stores the hash of the device spec that was last rendered
	DeviceAnnotationRenderedSpecHash = "device-controller/renderedSpecHash"
	// When this annotation is present, it means that the device has been selected for rollout in a batch
	DeviceAnnotationSelectedForRollout = "fleet-controller/selectedForRollout"
	DeviceAnnotationLastRolloutError   = "fleet-controller/lastRolloutError"

	// TODO: make configurable
	// DeviceDisconnectedTimeout is the duration after which a device is considered to be not reporting and set to unknown status.
	DeviceDisconnectedTimeout = 5 * time.Minute

	DeviceQueryConsoleSessionMetadata = "metadata"

	EnrollmentRequestAPIVersion = "v1beta1"
	EnrollmentRequestKind       = "EnrollmentRequest"
	EnrollmentRequestListKind   = "EnrollmentRequestList"

	FleetAPIVersion = "v1beta1"
	FleetKind       = "Fleet"
	FleetListKind   = "FleetList"

	FleetAnnotationTemplateVersion = "fleet-controller/templateVersion"
	// The last template version that has been processed by device selection reconciler.  It is used for new rollout detection
	FleetAnnotationDeployingTemplateVersion = "fleet-controller/deployingTemplateVersion"
	// The index to the current batch.  Contains an integer
	FleetAnnotationBatchNumber = "fleet-controller/batchNumber"
	// Indicates if the current batch has been approved
	FleetAnnotationRolloutApproved = "fleet-controller/rolloutApproved"
	// What is the active approval method: If automatic then it is based in the last batch success percentage.  Otherwise
	// it requires manual approval
	FleetAnnotationRolloutApprovalMethod = "fleet-controller/rolloutApprovalMethod"
	// A report specifying the completion report of the last batch
	FleetAnnotationLastBatchCompletionReport = "fleet-controller/lastBatchCompletionReport"
	// A frozen digest of device selection definition during rollout
	FleetAnnotationDeviceSelectionConfigDigest = "fleet-controller/deviceSelectionConfigDigest"
	// The requestID related to an event
	EventAnnotationRequestID = "event-controller/requestID"

	// AuthProvider annotation indicating it was created by a super admin
	AuthProviderAnnotationCreatedBySuperAdmin = "auth-provider/createdBySuperAdmin"

	RepositoryAPIVersion = "v1beta1"
	RepositoryKind       = "Repository"
	RepositoryListKind   = "RepositoryList"

	AuthProviderAPIVersion = "v1beta1"
	AuthProviderKind       = "AuthProvider"
	AuthProviderListKind   = "AuthProviderList"

	AuthConfigAPIVersion = "v1beta1"
	AuthConfigKind       = "AuthConfig"

	ResourceSyncAPIVersion = "v1beta1"
	ResourceSyncKind       = "ResourceSync"
	ResourceSyncListKind   = "ResourceSyncList"

	TemplateVersionAPIVersion = "v1beta1"
	TemplateVersionKind       = "TemplateVersion"
	TemplateVersionListKind   = "TemplateVersionList"

	EventAPIVersion = "v1beta1"
	EventKind       = "Event"
	EventListKind   = "EventList"

	EventAnnotationDelayDeviceRender = "fleet-controller/delayDeviceRender"

	OrganizationAPIVersion = "v1beta1"
	OrganizationKind       = "Organization"
	OrganizationListKind   = "OrganizationList"
	OrganizationIDQueryKey = "org_id"

	SystemKind           = "System"
	SystemComponentDB    = "database"
	SystemComponentQueue = "queue"

	// External role names - these come from authentication providers and are mapped to internal roles
	ExternalRoleAdmin     = "flightctl-admin"
	ExternalRoleOrgAdmin  = "flightctl-org-admin"
	ExternalRoleOperator  = "flightctl-operator"
	ExternalRoleViewer    = "flightctl-viewer"
	ExternalRoleInstaller = "flightctl-installer"

	// Internal role constants - used within flightctl for authorization
	RoleAdmin     = "admin"     // Full access to all resources
	RoleOrgAdmin  = "org-admin" // Full access to all resources within an organization
	RoleOperator  = "operator"  // Manage devices, fleets, resourcesyncs
	RoleViewer    = "viewer"    // Read-only access to devices, fleets, resourcesyncs
	RoleInstaller = "installer" // Limited access for device installation
)

var KnownExternalRoles = []string{ExternalRoleAdmin, ExternalRoleOrgAdmin, ExternalRoleOperator, ExternalRoleViewer, ExternalRoleInstaller}

type UpdateState string

const (
	// The agent is validating the desired device spec and downloading
	// dependencies. No changes have been made to the device's configuration
	// yet.
	UpdateStatePreparing UpdateState = "Preparing"
	//  The agent has validated the desired spec, downloaded all dependencies,
	//  and is ready to update. No changes have been made to the device's
	//  configuration yet.
	UpdateStateReadyToUpdate UpdateState = "ReadyToUpdate"
	// The agent has started the update transaction and is writing the update to
	// disk.
	UpdateStateApplyingUpdate UpdateState = "ApplyingUpdate"
	// The agent initiated a reboot required to activate the new OS image and configuration.
	UpdateStateRebooting UpdateState = "Rebooting"
	// The agent has successfully completed the update and the device is
	// conforming to its device spec. Note that the device's update status may
	// still be reported as `OutOfDate` if the device spec is not yet at the
	// same version as the fleet's device template
	UpdateStateUpdated UpdateState = "Updated"
	// The agent has canceled the update because the desired spec was reverted
	// to the current spec before the update process started.
	UpdateStateCanceled UpdateState = "Canceled"
	// The agent failed to apply the desired spec and will not retry. The
	// device's OS image and configuration have been rolled back to the
	// pre-update version and have been activated
	UpdateStateError UpdateState = "Error"
	// The agent has detected an error and is rolling back to the pre-update OS
	// image and configuration.
	UpdateStateRollingBack UpdateState = "RollingBack"
	// The agent failed to apply the desired spec and will retry. The device's
	// OS image and configuration have been rolled back to the pre-update
	// version and have been activated.
	UpdateStateRetrying UpdateState = "Retrying"
)

type DecommissionState string

const (
	// The agent has received the request to decommission from the service.
	DecommissionStateStarted DecommissionState = "Started"
	// The agent has completed its decommissioning actions.
	DecommissionStateComplete DecommissionState = "Completed"
	// The agent has encoutered an error while decommissioning.
	DecommissionStateError DecommissionState = "Error"
)

const (
	// No rollout is currently active
	RolloutInactiveReason = "Inactive"
	// Rollout is in progress
	RolloutActiveReason = "Active"
	// Rollout is suspended
	RolloutSuspendedReason = "Suspended"
	// Rollout is pending on user approval
	RolloutWaitingReason = "Waiting"

	// The name of the preliminary batch
	PreliminaryBatchName = "preliminary batch"
	// The name of the final implicit batch
	FinalImplicitBatchName = "final implicit batch"

	// System-level resource name for events
	FlightCtlSystemResourceName = "flightctl-system"

	// TPM Validation Reasons

	// TPMVerificationFailedReason indicates a TPM Request failed initial validation
	TPMVerificationFailedReason = "TPMVerificationFailed"
	// TPMChallengeRequiredReason indicates a TPM Challenge is required
	TPMChallengeRequiredReason = "TPMChallengeRequired"
	// TPMChallengeFailedReason indicates that a TPM Challenge attempt failed
	TPMChallengeFailedReason = "TPMChallengeFailed"
	// TPMChallengeSucceededReason indicates that a TPM Challenge attempt succeed
	TPMChallengeSucceededReason = "TPMChallengeSucceeded"

	// ResourceSync New Hash Detected Reason
	ResourceSyncNewHashDetectedReason = "NewHashDetected"
)

const (
	DeviceOutOfDateText          = "Device has not been updated to the latest device spec"
	DeviceOutOfSyncWithFleetText = "Device has not yet been scheduled for update to the fleet's latest spec."
)
