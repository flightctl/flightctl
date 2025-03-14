package common

import "github.com/flightctl/flightctl/internal/client"

func NewServiceConfig() ServiceConfig {
	return ServiceConfig{
		EnrollmentService: EnrollmentService{Config: *client.NewDefault()},
		ManagementService: ManagementService{Config: *client.NewDefault()},
	}
}

type ServiceConfig struct {
	// Enrollment is the client configuration for connecting to the device enrollment server
	EnrollmentService EnrollmentService `json:"enrollment-service,omitempty"`
	// Management is the client configuration for connecting to the device management server
	ManagementService ManagementService `json:"management-service,omitempty"`
}

type EnrollmentService struct {
	client.Config

	// EnrollmentUIEndpoint is the address of the device enrollment UI
	EnrollmentUIEndpoint string `json:"enrollment-ui-endpoint,omitempty"`
}

type ManagementService struct {
	client.Config
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
