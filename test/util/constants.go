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
	panic("unimplemented")
}

const (
	UpdateRenderedVersionSuccess  Message = "Updated to desired renderedVersion:"
	UpdateRenderedVersionProgress Message = "the device is upgrading to renderedVersion:"
)
