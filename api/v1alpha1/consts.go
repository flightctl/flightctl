package v1alpha1

import "time"

const (
	CertificateSigningRequestAPI      = "v1alpha1"
	CertificateSigningRequestKind     = "CertificateSigningRequest"
	CertificateSigningRequestListKind = "CertificateSigningRequestList"

	DeviceAPIVersion = "v1alpha1"
	DeviceKind       = "Device"
	DeviceListKind   = "DeviceList"

	DeviceAnnotationConsole                 = "device-controller/console"
	DeviceAnnotationRenderedVersion         = "device-controller/renderedVersion"
	DeviceAnnotationTemplateVersion         = "fleet-controller/templateVersion"
	DeviceAnnotationRenderedTemplateVersion = "device-controller/renderedTemplateVersion"
	DeviceAnnotationLastRolloutError        = "fleet-controller/lastRolloutError"

	// TODO: make configurable
	// DeviceDisconnectedTimeout is the duration after which a device is considered to be not reporting and set to unknown status.
	DeviceDisconnectedTimeout = 5 * time.Minute

	EnrollmentRequestAPIVersion = "v1alpha1"
	EnrollmentRequestKind       = "EnrollmentRequest"
	EnrollmentRequestListKind   = "EnrollmentRequestList"

	FleetAPIVersion = "v1alpha1"
	FleetKind       = "Fleet"
	FleetListKind   = "FleetList"

	FleetAnnotationTemplateVersion = "fleet-controller/templateVersion"

	RepositoryAPIVersion = "v1alpha1"
	RepositoryKind       = "Repository"
	RepositoryListKind   = "RepositoryList"

	ResourceSyncAPIVersion = "v1alpha1"
	ResourceSyncKind       = "ResourceSync"
	ResourceSyncListKind   = "ResourceSyncList"

	TemplateVersionAPIVersion = "v1alpha1"
	TemplateVersionKind       = "TemplateVersion"
	TemplateVersionListKind   = "TemplateVersionList"
)

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
