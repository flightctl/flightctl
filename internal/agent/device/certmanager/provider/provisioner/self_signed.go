package provisioner

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

// SelfSignedProvisionerConfig defines configuration for self-signed certificate provisioning.
// This provisioner creates a temporary certificate authority and uses it to sign certificates
// for testing, development, or bootstrap scenarios where no external CA is available.
type SelfSignedProvisionerConfig struct {
	CommonName        string `json:"common-name"`                  // Common name for the certificate
	ExpirationSeconds int    `json:"expiration-seconds,omitempty"` // Certificate validity period in seconds
}

// SelfSignedProvisioner creates self-signed certificates using a temporary certificate authority.
// It generates a new CA for each certificate request and uses it to sign the client certificate.
// This is primarily intended for testing and development scenarios.
type SelfSignedProvisioner struct {
	name   string                       // Name identifier for this provisioner
	cfg    *SelfSignedProvisionerConfig // Configuration for self-signed certificate generation
	keyPEM []byte                       // Generated private key in PEM format
	cert   *x509.Certificate            // Generated certificate
}

// NewSelfSignedProvisioner creates a new self-signed provisioner with the specified configuration.
// It initializes the provisioner but doesn't generate the certificate until Provision is called.
func NewSelfSignedProvisioner(cfg *SelfSignedProvisionerConfig) (*SelfSignedProvisioner, error) {
	return &SelfSignedProvisioner{
		name: cfg.CommonName,
		cfg:  cfg,
	}, nil
}

// Provision generates a self-signed certificate using a temporary certificate authority.
// It creates a new CA, generates a private key and CSR, then uses the CA to sign the certificate.
// This method always returns ready=true since self-signed certificates are generated synchronously.
func (p *SelfSignedProvisioner) Provision(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	if p.cfg.CommonName == "" {
		return false, nil, nil, fmt.Errorf("common name must be set for self-signed certificate")
	}

	tmpDir, err := os.MkdirTemp("", "self-signed-ca-*")
	if err != nil {
		return false, nil, nil, fmt.Errorf("failed to create temporary CA directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ca, err := fcrypto.MakeSelfSignedCA(filepath.Join(tmpDir, "ca.crt"), filepath.Join(tmpDir, "ca.key"), "", "self-signed-ca", 365)
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
type SelfSignedProvisionerFactory struct{}

// NewSelfSignedProvisionerFactory creates a new self-signed provisioner factory.
// This factory is stateless and requires no external dependencies.
func NewSelfSignedProvisionerFactory() *SelfSignedProvisionerFactory {
	return &SelfSignedProvisionerFactory{}
}

// Type returns the provisioner type string used as map key in the certificate manager.
func (f *SelfSignedProvisionerFactory) Type() string {
	return "self-signed"
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

	return NewSelfSignedProvisioner(&cfg)
}

// Validate checks whether the provided config is valid for a self-signed provisioner.
// It ensures the configuration can be properly decoded and contains valid settings.
func (f *SelfSignedProvisionerFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	prov := cc.Provisioner

	if prov.Type != "self-signed" {
		return fmt.Errorf("not a self-signed provisioner")
	}

	var cfg SelfSignedProvisionerConfig
	if err := json.Unmarshal(prov.Config, &cfg); err != nil {
		return fmt.Errorf("failed to decode self-signed provisioner config for certificate %q: %w", cc.Name, err)
	}

	return nil
}
