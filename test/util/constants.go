package util

import (
	"time"
)

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
	Device,
	Fleet,
	EnrollmentRequest,
	Repository,
	ResourceSync,
	CertificateSigningRequest,
}

const TIMEOUT = "5m"
const POLLING = "250ms"
const LONGTIMEOUT = "10m"
const DURATION_TIMEOUT = 5 * time.Minute

// Define a type for messages.
type Message string

func (m Message) String() string {
	return string(m)
}

const (
	UpdateRenderedVersionSuccess  Message = "Updated to desired renderedVersion:"
	UpdateRenderedVersionProgress Message = "the device is upgrading to renderedVersion:"
)

// URLsconst
const (
	FlightctlAnsibleRepoURL string = "https://github.com/flightctl/flightctl-ansible"
	DefaultMainBranch       string = "main"
)
const (
	AnsibleCollectionFLightCTLPath string = ".ansible/collections/ansible_collections/flightctl/core"
	AnsiblePlaybookFolderPath      string = "test/e2e/ansible/playbooks"
	AnsibleConfigFilePath          string = "test/e2e/ansible/playbooks/integration_config.yml"
	ClientConfigPath               string = ".config/flightctl/client.yaml"
)
