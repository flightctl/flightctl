package util

import "time"

// Resource types
const (
	Device                    = "device"
	Fleet                     = "fleet"
	EnrollmentRequest         = "enrollmentrequest"
	Repository                = "repository"
	ResourceSync              = "resourcesync"
	CertificateSigningRequest = "certificatesigningrequest"

	//resource related
	ApplyAction    = "apply"
	DeviceYAMLPath = "device.yaml"
	DeviceResource = "Device"
	RepoResource   = "Repository"
	ErResource     = "EnrollmentRequest"
	FleetResource  = "Fleet"

	// events
	ForceFlag    = "-f"
	EventCreated = "created"
	EventDeleted = "deleted"
	EventUpdated = "updated"
)

var ResourceTypes = [...]string{
	ResourceSync,
	Fleet,
	Device,
	EnrollmentRequest,
	Repository,
	CertificateSigningRequest,
}

const TIMEOUT = "5m"
const POLLING = "250ms"
const LONGTIMEOUT = "10m"
const DURATION_TIMEOUT = 5 * time.Minute

const E2E_NAMESPACE = "flightctl-e2e"
const E2E_REGISTRY_NAME = "registry"
const KIND = "KIND"
const OCP = "OCP"

// Define a type for messages.
type Message string

func (m Message) String() string {
	return string(m)
}

const (
	UpdateRenderedVersionSuccess  Message = "Updated to desired renderedVersion:"
	UpdateRenderedVersionProgress Message = "the device is upgrading to renderedVersion:"
)
