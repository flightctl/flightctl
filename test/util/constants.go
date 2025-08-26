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
	DeviceResource = "Device"
	RepoResource   = "Repository"
	ErResource     = "EnrollmentRequest"
	FleetResource  = "Fleet"
	SystemResource = "System"

	//example yaml names
	DeviceYAMLName = "device.yaml"
	FleetYAMLName  = "fleet.yaml"
	FleetBYAMLName = "fleet-b.yaml"
	RepoYAMLName   = "repository-flightctl.yaml"
	ErYAMLName     = "enrollmentrequest.yaml"

	// events
	ForceFlag    = "-f"
	EventCreated = "created"
	EventDeleted = "deleted"
	EventUpdated = "updated"

	//Event reasons
	ResourceCreated          = "ResourceCreated"
	DeviceApplicationError   = "DeviceApplicationError"
	DeviceApplicationHealthy = "DeviceApplicationHealthy"
	DeviceSpecInvalid        = "DeviceSpecInvalid"
	DeviceSpecValid          = "DeviceSpecValid"
	DeviceContentOutOfDate   = "DeviceContentOutOfDate"
	DeviceContentUpToDate    = "DeviceContentUpToDate"
	DeviceUpdateFailed       = "DeviceUpdateFailed"

	// Eventually polling timeout/interval constants
	TIMEOUT      = time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 10 * time.Second

	DURATION_TIMEOUT = 5 * time.Minute
	SHORT_POLLING    = "250ms"
	TIMEOUT_5M       = "5m"
	LONGTIMEOUT      = "10m"
)

var ResourceTypes = [...]string{
	ResourceSync,
	Fleet,
	Device,
	EnrollmentRequest,
	Repository,
	CertificateSigningRequest,
}

// RequiredSystemInfoKeys defines the required system info keys that should be present
var DefaultSystemInfo = []string{
	"hostname",
	"kernel",
	"distroName",
	"distroVersion",
	"productName",
	"productUuid",
	"productSerial",
	"netInterfaceDefault",
	"netIpDefault",
	"netMacDefault",
}

const E2E_NAMESPACE = "flightctl-e2e"
const E2E_REGISTRY_NAME = "registry"
const KIND = "KIND"
const OCP = "OCP"
const FLIGHTCTL_AGENT_SERVICE = "flightctl-agent"

// Define a type for messages.
type Message string

func (m Message) String() string {
	return string(m)
}

const (
	UpdateRenderedVersionSuccess  Message = "Updated to desired renderedVersion:"
	UpdateRenderedVersionProgress Message = "the device is upgrading to renderedVersion:"
)
