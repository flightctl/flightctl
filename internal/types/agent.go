package types

import (
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/util"
)

type AgentConfig struct {
	// ConfigDir is the directory where the device's configuration is stored
	ConfigDir string `json:"-"`
	// DataDir is the directory where the device's data is stored
	DataDir string `json:"-"`

	// EnrollmentService is the client configuration for connecting to the device enrollment server
	EnrollmentService EnrollmentService `json:"enrollment-service,omitempty"`
	// ManagementService is the client configuration for connecting to the device management server
	ManagementService ManagementService `json:"management-service,omitempty"`

	// SpecFetchInterval is the interval between two reads of the remote device spec
	SpecFetchInterval util.Duration `json:"spec-fetch-interval,omitempty"`
	// StatusUpdateInterval is the interval between two status updates
	StatusUpdateInterval util.Duration `json:"status-update-interval,omitempty"`

	// TPMPath is the path to the TPM device
	TPMPath string `json:"tpm-path,omitempty"`

	// LogLevel is the level of logging. can be:  "panic", "fatal", "error", "warn"/"warning",
	// "info", "debug" or "trace", any other will be treated as "info"
	LogLevel string `json:"log-level,omitempty"`
	// LogPrefix is the log prefix used for testing
	LogPrefix string `json:"log-prefix,omitempty"`

	// DefaultLabels are automatically applied to this device when the agent is enrolled in a service
	DefaultLabels map[string]string `json:"default-labels,omitempty"`
}

type EnrollmentService struct {
	baseclient.Config

	// EnrollmentUIEndpoint is the address of the device enrollment UI
	EnrollmentUIEndpoint string `json:"enrollment-ui-endpoint,omitempty"`
}

type ManagementService struct {
	baseclient.Config
}

func (s *EnrollmentService) Equal(s2 *EnrollmentService) bool {
	if s == s2 {
		return true
	}
	return s.Config.Equal(&s2.Config) && s.EnrollmentUIEndpoint == s2.EnrollmentUIEndpoint
}

func (s *ManagementService) Equal(s2 *ManagementService) bool {
	if s == s2 {
		return true
	}
	return s.Config.Equal(&s2.Config)
}
