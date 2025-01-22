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
