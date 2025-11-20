package initialization

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/sirupsen/logrus"
)

var (
	ErrReadingProvidedCerts   = errors.New("unable to read provided certificate files")
	ErrReadingDefaultCerts    = errors.New("unable to read default certificate files")
	ErrGeneratingDefaultCerts = errors.New("unable to generate default certificate files")
)

// ServerCertificates handles the initialization of CA and server certificates.
// It either loads or generates the certificates if they do not exist.
func ServerCertificates(ctx context.Context, cfg *config.Config, log *logrus.Logger) (*crypto.CAClient, *crypto.TLSCertificateConfig, error) {
	ca, _, err := crypto.EnsureCA(cfg.CA)
	if err != nil {
		return nil, nil, fmt.Errorf("loading or generating CA: %w", err)
	}

	var serverCerts *crypto.TLSCertificateConfig
	if cfg.Service.SrvCertFile != "" || cfg.Service.SrvKeyFile != "" {
		serverCerts, err = loadExistingServerCertificates(cfg.Service.SrvCertFile, cfg.Service.SrvKeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrReadingProvidedCerts, err)
		}
		if serverCerts == nil {
			return nil, nil, ErrReadingProvidedCerts
		}
	} else {
		serverCertFileName := crypto.CertStorePath(cfg.Service.ServerCertName+".crt", cfg.Service.CertStore)
		serverKeyFileName := crypto.CertStorePath(cfg.Service.ServerCertName+".key", cfg.Service.CertStore)

		serverCerts, err = loadExistingServerCertificates(serverCertFileName, serverKeyFileName)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrReadingDefaultCerts, err)
		}
		if serverCerts == nil {
			// Default to localhost if no alternative names are set
			altNames := cfg.Service.AltNames
			if len(altNames) == 0 {
				altNames = []string{"localhost"}
			}
			serverCerts, err = ca.MakeAndWriteServerCertificate(ctx, serverCertFileName, serverKeyFileName, altNames, cfg.CA.ServerCertValidityDays)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: %v", ErrGeneratingDefaultCerts, err)
			}
		}
	}

	// Check for expired certificate
	for _, x509Cert := range serverCerts.Certs {
		expired := time.Now().After(x509Cert.NotAfter)
		log.Printf("checking certificate: subject='%s', issuer='%s', expiry='%v'",
			x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)

		if expired {
			log.Warnf("server certificate for '%s' issued by '%s' has expired on: %v",
				x509Cert.Subject.CommonName, x509Cert.Issuer.CommonName, x509Cert.NotAfter)
		}
	}

	return ca, serverCerts, nil
}

func loadExistingServerCertificates(certFileName, keyFileName string) (*crypto.TLSCertificateConfig, error) {
	canReadCertAndKey, err := crypto.CanReadCertAndKey(certFileName, keyFileName)
	if err != nil {
		return nil, err
	}

	if !canReadCertAndKey {
		return nil, nil
	}

	serverCerts, err := crypto.GetTLSCertificateConfig(certFileName, keyFileName)
	if err != nil {
		return nil, err
	}
	return serverCerts, nil
}

func BootstrapClientCertificates(ctx context.Context, cfg *config.Config, ca *crypto.CAClient) error {
	clientCertFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".crt", cfg.Service.CertStore)
	clientKeyFile := crypto.CertStorePath(cfg.CA.ClientBootstrapCertName+".key", cfg.Service.CertStore)
	_, _, err := ca.EnsureClientCertificate(ctx, clientCertFile, clientKeyFile, cfg.CA.ClientBootstrapCommonName, cfg.CA.ClientBootstrapValidityDays)
	return err
}
