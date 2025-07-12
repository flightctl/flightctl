package provisioner

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
	fcrypto "github.com/flightctl/flightctl/internal/crypto"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
)

type SelfSignedProvisionerConfig struct {
	CommonName        string `json:"common-name"`
	ExpirationSeconds int    `json:"expiration-seconds,omitempty"`
}

type SelfSignedProvisioner struct {
	name   string
	cfg    *SelfSignedProvisionerConfig
	keyPEM []byte
	cert   *x509.Certificate
}

func NewSelfSignedProvisioner(cfg *SelfSignedProvisionerConfig) (*SelfSignedProvisioner, error) {
	return &SelfSignedProvisioner{
		name: cfg.CommonName,
		cfg:  cfg,
	}, nil
}

func (p *SelfSignedProvisioner) Provision(ctx context.Context) error {
	if p.cfg.CommonName == "" {
		return fmt.Errorf("common name must be set for self-signed certificate")
	}

	tmpDir, err := os.MkdirTemp("", "self-signed-ca-*")
	if err != nil {
		return fmt.Errorf("failed to create temporary CA directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ca, err := fcrypto.MakeSelfSignedCA(filepath.Join(tmpDir, "ca.crt"), filepath.Join(tmpDir, "ca.key"), "", "self-signed-ca", 365)
	if err != nil {
		return err
	}

	validity := 365 * 24 * 3600 // default 1 year in seconds
	if p.cfg.ExpirationSeconds > 0 {
		validity = p.cfg.ExpirationSeconds
	}

	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return fmt.Errorf("failed to generate key: %w", err)
	}

	signer, ok := priv.(crypto.Signer)
	if !ok {
		return fmt.Errorf("expected crypto.Signer, got %T", priv)
	}

	csr, err := fccrypto.MakeCSR(signer, p.cfg.CommonName)
	if err != nil {
		return err
	}

	req, err := fccrypto.ParseCSR(csr)
	if err != nil {
		return err
	}

	cert, err := ca.IssueRequestedCertificateAsX509(ctx, req, validity, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return fmt.Errorf("failed to create self-signed cert: %w", err)
	}

	keyPEM, err := fccrypto.PEMEncodeKey(signer)
	if err != nil {
		return fmt.Errorf("failed to encode key: %w", err)
	}

	p.cert = cert
	p.keyPEM = keyPEM
	return nil
}

func (p *SelfSignedProvisioner) Check(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	return true, p.cert, p.keyPEM, nil
}

type SelfSignedProvisionerFactory struct{}

func NewSelfSignedProvisionerFactory() *SelfSignedProvisionerFactory {
	return &SelfSignedProvisionerFactory{}
}

func (f *SelfSignedProvisionerFactory) Type() string {
	return "self-signed"
}

func (f *SelfSignedProvisionerFactory) New(log common.Logger, cc common.CertificateConfig) (common.ProvisionerProvider, error) {
	prov := cc.Provisioner

	var cfg SelfSignedProvisionerConfig
	if err := json.Unmarshal(prov.Config, &cfg); err != nil {
		return nil, fmt.Errorf("failed to decode self-signed config for certificate %q: %w", cc.Name, err)
	}

	if cfg.CommonName == "" {
		cfg.CommonName = cc.Name
	}

	return NewSelfSignedProvisioner(&cfg)
}

func (f *SelfSignedProvisionerFactory) Validate(log common.Logger, cc common.CertificateConfig) error {
	prov := cc.Provisioner

	if prov.Type != "self-signed" {
		return fmt.Errorf("not a self-signed provisioner")
	}

	var cfg SelfSignedProvisionerConfig
	if err := json.Unmarshal(prov.Config, &cfg); err != nil {
		return fmt.Errorf("failed to decode self-signed config for certificate %q: %w", cc.Name, err)
	}

	return nil
}
