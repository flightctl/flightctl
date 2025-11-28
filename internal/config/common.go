package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/sirupsen/logrus"
)

var (
	ErrCheckingServerCerts = errors.New("failed to check if server certificate and key can be read")
	ErrServerCertsNotFound = errors.New("server certificate and key files are missing or unreadable")
	ErrInvalidServerCerts  = errors.New("failed to parse or load server certificate and key")
)

type ServiceConfig struct {
	// EnrollmentService is the client configuration for connecting to the device enrollment server
	EnrollmentService EnrollmentService `json:"enrollment-service,omitempty"`
	// ManagementService is the client configuration for connecting to the device management server
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

func NewServiceConfig() ServiceConfig {
	return ServiceConfig{
		EnrollmentService: EnrollmentService{Config: *client.NewDefault()},
		ManagementService: ManagementService{Config: *client.NewDefault()},
	}
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

func LoadServerCertificates(cfg *Config, log *logrus.Logger) (*crypto.TLSCertificateConfig, error) {
	var keyFile, certFile string
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		certFile = cfg.Service.SrvCertFile
		keyFile = cfg.Service.SrvKeyFile
	} else {
		certFile = crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		keyFile = crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)
	}

	canReadCertAndKey, err := crypto.CanReadCertAndKey(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCheckingServerCerts, err)
	}
	if !canReadCertAndKey {
		return nil, ErrServerCertsNotFound
	}

	serverCerts, err := crypto.GetTLSCertificateConfig(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidServerCerts, err)
	}

	// check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		log.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			log.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	return serverCerts, nil
}
