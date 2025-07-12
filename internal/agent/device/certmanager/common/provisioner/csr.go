package provisioner

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

// CSRProvisionerConfig defines CSR-specific provisioner configuration
type CSRProvisionerConfig struct {
	// Signer is the name of the signer for CSR provisioning
	Signer string `json:"signer"`
	// CommonName is the common name for the certificate
	CommonName string `json:"common-name,omitempty"`
	// KeyUsage specifies the key usage for the certificate (e.g., "clientAuth", "serverAuth")
	KeyUsage []string `json:"key-usage,omitempty"`
	// ExpirationSeconds requests a specific certificate validity duration (in seconds); signer may ignore
	ExpirationSeconds *int32 `json:"expiration-seconds,omitempty"`
	// Additional CSR-specific configuration (future extensions)
	Config map[string]interface{} `json:"config,omitempty"`
}

type CSRProvisioner struct {
	deviceName       string
	managementClient client.Management
	cfg              *CSRProvisionerConfig

	csrName    string
	privateKey crypto.Signer
}

func NewCSRProvisioner(deviceName string, managementClient client.Management, cfg *CSRProvisionerConfig) (*CSRProvisioner, error) {
	return &CSRProvisioner{
		deviceName:       deviceName,
		managementClient: managementClient,
		cfg:              cfg,
	}, nil
}

func (p *CSRProvisioner) Provision(ctx context.Context) error {
	if p.cfg.CommonName == "" {
		return fmt.Errorf("commonName must be set")
	}

	// Generate unique CSR object name for Kubernetes resource
	p.csrName = fmt.Sprintf("%s-%s", p.cfg.CommonName, uuid.NewString()[:8])

	// Generate private key and CSR using the configured CommonName (without suffix)
	key, csr, err := generateKeyAndCSR(p.cfg.CommonName)
	if err != nil {
		return fmt.Errorf("generate csr: %w", err)
	}

	p.privateKey = key

	req := api.CertificateSigningRequest{
		ApiVersion: api.CertificateSigningRequestAPIVersion,
		Kind:       api.CertificateSigningRequestKind,
		Metadata: api.ObjectMeta{
			Name: &p.csrName,
		},
		Spec: api.CertificateSigningRequestSpec{
			ExpirationSeconds: p.cfg.ExpirationSeconds, // <- Use directly from your CSRProvisionerConfig
			Request:           csr,
			SignerName:        p.cfg.Signer,
			Usages:            &p.cfg.KeyUsage,
		},
	}
	_, statusCode, err := p.managementClient.CreateCertificateSigningRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("create csr: %w", err)
	}

	switch statusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	default:
		return fmt.Errorf("%w: unexpected status code %d", errors.ErrCreateCertificateSigningRequest, statusCode)
	}
}

func (p *CSRProvisioner) Check(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	if p.csrName == "" {
		return false, nil, nil, fmt.Errorf("no CSR name recorded")
	}
	if p.privateKey == nil {
		return false, nil, nil, fmt.Errorf("no private key generated")
	}

	csr, statusCode, err := p.managementClient.GetCertificateSigningRequest(ctx, p.csrName)
	if err != nil {
		return false, nil, nil, fmt.Errorf("get csr: %w", err)
	}
	if statusCode != http.StatusOK {
		return false, nil, nil, fmt.Errorf("unexpected status code %d while fetching CSR %q", statusCode, p.csrName)
	}
	if csr == nil {
		return false, nil, nil, fmt.Errorf("received nil CSR object for %q", p.csrName)
	}
	if csr.Status == nil {
		return false, nil, nil, nil // Not ready yet, wait for status to be populated
	}

	if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestApproved) && csr.Status.Certificate != nil {
		certPEM := *csr.Status.Certificate

		cert, err := fccrypto.ParsePEMCertificate(certPEM)
		if err != nil {
			return false, nil, nil, fmt.Errorf("failed to parse CSR PEM certificate: %w", err)
		}

		keyPEM, err := fccrypto.PEMEncodeKey(p.privateKey)
		if err != nil {
			return false, nil, nil, fmt.Errorf("failed to encode private key: %w", err)
		}

		return true, cert, keyPEM, nil
	}

	if api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestDenied) ||
		api.IsStatusConditionTrue(csr.Status.Conditions, api.ConditionTypeCertificateSigningRequestFailed) {
		return false, nil, nil, fmt.Errorf("csr %q was denied or failed", p.csrName)
	}

	return false, nil, nil, nil // still pending
}

func generateKeyAndCSR(commonName string) (crypto.Signer, []byte, error) {
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to generate key pair: %w", err)
	}

	signer, ok := priv.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("expected crypto.Signer, got %T", priv)
	}

	csr, err := fccrypto.MakeCSR(signer, commonName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create CSR: %w", err)
	}

	return signer, csr, nil
}

// CSRProvisionerFactory implements ProvisionerFactory for CSR-based provisioners.
type CSRProvisionerFactory struct {
	deviceName       string
	managementClient client.Management
}

// NewCSRProvisionerFactory creates a new CSRProvisionerFactory.
func NewCSRProvisionerFactory(deviceName string, managementClient client.Management) *CSRProvisionerFactory {
	return &CSRProvisionerFactory{
		deviceName:       deviceName,
		managementClient: managementClient,
	}
}

// Type returns the provisioner type string used as map key.
func (f *CSRProvisionerFactory) Type() string {
	return "csr"
}

// New creates a new CSRProvisioner based on the provided certificate config.
func (f *CSRProvisionerFactory) New(log common.Logger, cc common.CertificateConfig) (common.ProvisionerProvider, error) {
	prov := cc.Provisioner

	var csrConfig CSRProvisionerConfig
	if err := json.Unmarshal(prov.Config, &csrConfig); err != nil {
		return nil, fmt.Errorf("failed to decode CSR config for certificate %q: %w", cc.Name, err)
	}

	// Resolve common name, replacing placeholders.
	commonName := csrConfig.CommonName
	if commonName == "" {
		commonName = cc.Name
	}

	commonName = strings.ReplaceAll(commonName, "${DEVICE_ID}", f.deviceName)
	if strings.Contains(commonName, "${DEVICE_ID}") {
		return nil, fmt.Errorf("commonName placeholder ${DEVICE_ID} not fully replaced in certificate %q", cc.Name)
	}

	csrConfig.CommonName = commonName
	return NewCSRProvisioner(f.deviceName, f.managementClient, &csrConfig)
}

// Validate checks whether the provided config is valid for a CSR provisioner.
func (f *CSRProvisionerFactory) Validate(log common.Logger, cc common.CertificateConfig) error {
	prov := cc.Provisioner

	if prov.Type != "csr" {
		return fmt.Errorf("not a CSR provisioner")
	}

	var csrConfig CSRProvisionerConfig
	if err := json.Unmarshal(prov.Config, &csrConfig); err != nil {
		return fmt.Errorf("failed to decode CSR config for certificate %q: %w", cc.Name, err)
	}

	if csrConfig.Signer == "" {
		return fmt.Errorf("signer must be specified for CSR provisioner in certificate %q", cc.Name)
	}

	return nil
}
