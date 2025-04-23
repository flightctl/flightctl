package util

// Resource types
const (
	Device                    = "Device"
	Fleet                     = "Fleet"
	EnrollmentRequest         = "EnrollmentRequest"
	Repository                = "Repository"
	ResourceSync              = "ResourceSync"
	CertificateSigningRequest = "CertificateSigningRequest"
)

var ResourceTypes = [...]string{Device, Fleet, EnrollmentRequest, Repository, ResourceSync, CertificateSigningRequest}

const TIMEOUT = "5m"
const POLLING = "250ms"
const LONGTIMEOUT = "10m"

// Define a type for messages.
type Message string

func (m Message) String() string {
	return string(m)
}

const (
	UpdateRenderedVersionSuccess  Message = "Updated to desired renderedVersion:"
	UpdateRenderedVersionProgress Message = "the device is upgrading to renderedVersion:"
)

const (
	// Ansible Galaxy collection name
	AnsibleGalaxyCollection   = "flightctl.core"
	AnsibleResourceInfoModule = "flightctl_resource_info"
	AnsibleResourceModule     = "flightctl_resource"
)
