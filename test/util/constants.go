package util

// Resource types
const (
	Device                    = "device"
	Fleet                     = "fleet"
	EnrollmentRequest         = "enrollmentrequest"
	Repository                = "repository"
	ResourceSync              = "resourcesync"
	CertificateSigningRequest = "certificatesigningrequest"
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

// Resource types
const (
	AnsibleDevice                    = "Device"
	AnsibleFleet                     = "Fleet"
	AnsibleEnrollmentRequest         = "EnrollmentRequest"
	AnsibleRepository                = "Repository"
	AnsibleResourceSync              = "ResourceSync"
	AnsibleCertificateSigningRequest = "CertificateSigningRequest"
)

var AnsibleResourceTypes = [...]string{AnsibleDevice, AnsibleFleet, AnsibleEnrollmentRequest, AnsibleRepository, AnsibleResourceSync, AnsibleCertificateSigningRequest}

const (
	// Ansible Galaxy collection name
	AnsibleGalaxyCollection   = "flightctl.core"
	AnsibleResourceInfoModule = "flightctl_resource_info"
	AnsibleResourceModule     = "flightctl_resource"
)
