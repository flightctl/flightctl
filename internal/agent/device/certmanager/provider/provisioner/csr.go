package provisioner

import (
	"bytes"
	"context"
	"crypto"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"text/template"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	agentapi "github.com/flightctl/flightctl/internal/api/client/agent"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

// csrClient is the minimal management client surface required by the CSR provisioner.
type csrClient interface {
	CreateCertificateSigningRequest(ctx context.Context, csr api.CertificateSigningRequest, rcb ...agentapi.RequestEditorFn) (*api.CertificateSigningRequest, int, error)
	GetCertificateSigningRequest(ctx context.Context, name string, rcb ...agentapi.RequestEditorFn) (*api.CertificateSigningRequest, int, error)
}

// CSRProvisionerConfig defines configuration for Certificate Signing Request (CSR) based provisioning.
// This provisioner generates a private key and CSR, submits it to the management server,
// and waits for approval and signing by the certificate authority.
type CSRProvisionerConfig struct {
	// Signer is the name of the signer for CSR provisioning
	Signer string `json:"signer"`
	// CommonName is the common name for the certificate
	CommonName string `json:"common-name,omitempty"`
	// Usages specifies a set of key usages requested in the issued certificate (e.g., "clientAuth", "serverAuth")
	Usages []string `json:"usages,omitempty"`
	// ExpirationSeconds requests a specific certificate validity duration (in seconds); signer may ignore
	ExpirationSeconds *int32 `json:"expiration-seconds,omitempty"`
	// Additional CSR-specific configuration (future extensions)
	Config map[string]interface{} `json:"config,omitempty"`
}

// CSRProvisioner handles certificate provisioning through Certificate Signing Requests.
// It generates a private key and CSR, submits it to the management server, and polls
// for approval and certificate issuance. This supports the standard Kubernetes CSR workflow.
type CSRProvisioner struct {
	// Name of the device requesting the certificate
	deviceName string
	// Client for communicating with management server
	csrClient csrClient
	// Configuration for CSR provisioning
	cfg *CSRProvisionerConfig

	// Name of the CSR resource on the server
	csrName string
	// Generated private key for this certificate
	privateKey crypto.Signer
}

// NewCSRProvisioner creates a new CSR provisioner with the specified configuration.
func NewCSRProvisioner(deviceName string, csrClient csrClient, cfg *CSRProvisionerConfig) (*CSRProvisioner, error) {
	return &CSRProvisioner{
		deviceName: deviceName,
		csrClient:  csrClient,
		cfg:        cfg,
	}, nil
}

// Provision attempts to provision a certificate through the CSR workflow.
// On first call, it generates a private key and submits a CSR to the server.
// On subsequent calls, it checks the CSR status and returns the certificate when approved.
// Returns ready=true when certificate is available, ready=false when still processing.
func (p *CSRProvisioner) Provision(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	if p.csrName != "" {
		return p.check(ctx)
	}

	if p.cfg.CommonName == "" {
		return false, nil, nil, fmt.Errorf("commonName must be set")
	}

	// Generate unique CSR object name for Kubernetes resource
	p.csrName = fmt.Sprintf("%s-%s", p.cfg.CommonName, uuid.NewString()[:8])

	// Generate private key and CSR using the configured CommonName (without suffix)
	key, csr, err := generateKeyAndCSR(p.cfg.CommonName)
	if err != nil {
		return false, nil, nil, fmt.Errorf("generate csr: %w", err)
	}

	p.privateKey = key

	usages := []string{
		"clientAuth",
		"CA:false",
	}

	if len(p.cfg.Usages) > 0 {
		usages = append(usages, p.cfg.Usages...)
	}

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
			Usages:            &usages,
		},
	}
	_, statusCode, err := p.csrClient.CreateCertificateSigningRequest(ctx, req)
	if err != nil {
		return false, nil, nil, fmt.Errorf("create csr: %w", err)
	}

	switch statusCode {
	case http.StatusOK, http.StatusCreated:
		return false, nil, nil, nil
	default:
		return false, nil, nil, fmt.Errorf("%w: unexpected status code %d", errors.ErrCreateCertificateSigningRequest, statusCode)
	}
}

// check polls the management server for CSR status and returns the certificate when ready.
// It handles the different CSR states: pending, approved, denied, or failed.
func (p *CSRProvisioner) check(ctx context.Context) (bool, *x509.Certificate, []byte, error) {
	if p.csrName == "" {
		return false, nil, nil, fmt.Errorf("no CSR name recorded")
	}
	if p.privateKey == nil {
		return false, nil, nil, fmt.Errorf("no private key generated")
	}

	csr, statusCode, err := p.csrClient.GetCertificateSigningRequest(ctx, p.csrName)
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

// generateKeyAndCSR generates a new private key and creates a CSR with the specified common name.
// This is used internally by the CSR provisioner to create the initial certificate request.
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
// It creates CSR provisioners with device-specific configuration and validates CSR configs.
type CSRProvisionerFactory struct {
	// Name of the device for CSR common name substitution
	deviceName string
	// Client for communicating with management server
	managementClient csrClient
}

// NewCSRProvisionerFactory creates a new CSRProvisionerFactory with the specified dependencies.
func NewCSRProvisionerFactory(deviceName string, managementClient csrClient) *CSRProvisionerFactory {
	return &CSRProvisionerFactory{
		deviceName:       deviceName,
		managementClient: managementClient,
	}
}

// Type returns the provisioner type string used as map key in the certificate manager.
func (f *CSRProvisionerFactory) Type() string {
	return string(provider.ProvisionerTypeCSR)
}

// New creates a new CSRProvisioner based on the provided certificate config.
// It decodes the CSR-specific configuration and performs common name substitution.
func (f *CSRProvisionerFactory) New(log provider.Logger, cc provider.CertificateConfig) (provider.ProvisionerProvider, error) {
	prov := cc.Provisioner

	var csrConfig CSRProvisionerConfig
	if err := json.Unmarshal(prov.Config, &csrConfig); err != nil {
		return nil, fmt.Errorf("failed to decode CSR provisioner config for certificate %q: %w", cc.Name, err)
	}

	commonName := csrConfig.CommonName
	if commonName == "" {
		commonName = cc.Name
	}

	tmpl, err := template.New("commonName").Parse(commonName)
	if err != nil {
		return nil, fmt.Errorf("failed to parse commonName template for certificate %q: %w", cc.Name, err)
	}

	templateData := map[string]string{
		"DEVICE_ID": f.deviceName,
	}

	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, templateData); err != nil {
		return nil, fmt.Errorf("failed to render commonName template for certificate %q: %w", cc.Name, err)
	}

	csrConfig.CommonName = rendered.String()

	return NewCSRProvisioner(f.deviceName, f.managementClient, &csrConfig)
}

// Validate checks whether the provided config is valid for a CSR provisioner.
// It ensures required fields are present and the configuration is properly formatted.
func (f *CSRProvisionerFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	prov := cc.Provisioner

	if prov.Type != provider.ProvisionerTypeCSR {
		return fmt.Errorf("not a CSR provisioner")
	}

	var csrConfig CSRProvisionerConfig
	if err := json.Unmarshal(prov.Config, &csrConfig); err != nil {
		return fmt.Errorf("failed to decode CSR provisioner config for certificate %q: %w", cc.Name, err)
	}

	if csrConfig.Signer == "" {
		return fmt.Errorf("signer must be specified for CSR provisioner in certificate %q", cc.Name)
	}

	return nil
}
