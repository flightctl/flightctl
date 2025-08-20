package provisioner

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

// SelfSignedProvisionerConfig defines configuration for self-signed certificate provisioning.
// This provisioner creates a temporary certificate authority and uses it to sign certificates
// for testing, development, or bootstrap scenarios where no external CA is available.
type SelfSignedProvisionerConfig struct {
	// Common name for the certificate
	CommonName string `json:"common-name"`
	// Certificate validity period in seconds
	ExpirationSeconds int `json:"expiration-seconds,omitempty"`
}

// SelfSignedProvisioner creates self-signed certificates using a temporary certificate authority.
// It generates a new CA for each certificate request and uses it to sign the client certificate.
// This is primarily intended for testing and development scenarios.
type SelfSignedProvisioner struct {
	// Name identifier for this provisioner
	name string
	// Configuration for self-signed certificate generation
	cfg *SelfSignedProvisionerConfig
	// File I/O for temp dirs and paths (test-friendly)
	rw fileio.ReadWriter
	// Logger for diagnostics
	log provider.Logger
	// Generated private key in PEM format
	keyPEM []byte
	// Generated certificate
	cert *x509.Certificate
}

// NewSelfSignedProvisioner creates a new self-signed provisioner with the specified configuration.
// It initializes the provisioner but doesn't generate the certificate until Provision is called.
func NewSelfSignedProvisioner(cfg *SelfSignedProvisionerConfig, rw fileio.ReadWriter, log provider.Logger) (*SelfSignedProvisioner, error) {
	return &SelfSignedProvisioner{
		name: cfg.CommonName,
		cfg:  cfg,
		rw:   rw,
		log:  log,
	}, nil
}

// Provision generates a self-signed certificate using a temporary certificate authority.
// It creates a new CA, generates a private key and CSR, then uses the CA to sign the certificate.
// This method always returns ready=true since self-signed certificates are generated synchronously.
func (p *SelfSignedProvisioner) Provision(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	if p.cfg.CommonName == "" {
		return false, nil, nil, fmt.Errorf("common name must be set for self-signed certificate")
	}

	// create temp dir under writer root
	tmpDir, err := p.rw.MkdirTemp("self-signed-ca-")
	if err != nil {
		return false, nil, nil, fmt.Errorf("failed to create temporary CA directory: %w", err)
	}
	defer func() {
		if err := p.rw.RemoveAll(tmpDir); err != nil && p.log != nil {
			p.log.Warnf("failed to cleanup temporary directory %s: %v", tmpDir, err)
		}
	}()

	fullTmp := p.rw.PathFor(tmpDir)
	ca, err := fcrypto.MakeSelfSignedCA(filepath.Join(fullTmp, "ca.crt"), filepath.Join(fullTmp, "ca.key"), "", "self-signed-ca", 365)
	if err != nil {
		return false, nil, nil, err
	}

	validity := 365 * 24 * 3600 // default 1 year in seconds
	if p.cfg.ExpirationSeconds > 0 {
		validity = p.cfg.ExpirationSeconds
	}

	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return false, nil, nil, fmt.Errorf("failed to generate key: %w", err)
	}

	signer, ok := priv.(crypto.Signer)
	if !ok {
		return false, nil, nil, fmt.Errorf("expected crypto.Signer, got %T", priv)
	}

	csr, err := fccrypto.MakeCSR(signer, p.cfg.CommonName)
	if err != nil {
		return false, nil, nil, err
	}

	req, err := fccrypto.ParseCSR(csr)
	if err != nil {
		return false, nil, nil, err
	}

	cert, err := ca.IssueRequestedCertificateAsX509(ctx, req, validity, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return false, nil, nil, fmt.Errorf("failed to create self-signed cert: %w", err)
	}

	keyPEM, err := fccrypto.PEMEncodeKey(signer)
	if err != nil {
		return false, nil, nil, fmt.Errorf("failed to encode key: %w", err)
	}

	p.cert = cert
	p.keyPEM = keyPEM
	return true, cert, keyPEM, nil
}

// SelfSignedProvisionerFactory implements ProvisionerFactory for self-signed provisioners.
// It creates self-signed provisioners that generate certificates using temporary certificate authorities.
type SelfSignedProvisionerFactory struct{ rw fileio.ReadWriter }

// NewSelfSignedProvisionerFactory creates a new self-signed provisioner factory.
// This factory is stateless and requires no external dependencies.
func NewSelfSignedProvisionerFactory(rw fileio.ReadWriter) *SelfSignedProvisionerFactory {
	return &SelfSignedProvisionerFactory{rw: rw}
}

// Type returns the provisioner type string used as map key in the certificate manager.
func (f *SelfSignedProvisionerFactory) Type() string {
	return string(provider.ProvisionerTypeSelfSigned)
}

// New creates a new SelfSignedProvisioner based on the provided certificate config.
// It decodes the self-signed specific configuration and sets default values as needed.
func (f *SelfSignedProvisionerFactory) New(log provider.Logger, cc provider.CertificateConfig) (provider.ProvisionerProvider, error) {
	prov := cc.Provisioner

	var cfg SelfSignedProvisionerConfig
	if err := json.Unmarshal(prov.Config, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode self-signed provisioner config for certificate %q: %w", cc.Name, err)
	}

	if cfg.CommonName == "" {
		cfg.CommonName = cc.Name
	}

	return NewSelfSignedProvisioner(&cfg, f.rw, log)
}

// Validate checks whether the provided config is valid for a self-signed provisioner.
// It ensures the configuration can be properly decoded and contains valid settings.
func (f *SelfSignedProvisionerFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	prov := cc.Provisioner

	if prov.Type != provider.ProvisionerTypeSelfSigned {
		return fmt.Errorf("not a self-signed provisioner")
	}

	var cfg SelfSignedProvisionerConfig
	if err := json.Unmarshal(prov.Config, &cfg); err != nil {
		return fmt.Errorf("failed to decode self-signed provisioner config for certificate %q: %w", cc.Name, err)
	}

	return nil
}
