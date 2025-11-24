package identity

import (
	"context"
	"crypto"
	"encoding/base32"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	base_client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
)

var (
	// ErrNotInitialized indicates the provider has not been initialized
	ErrNotInitialized = errors.New("identity provider not initialized")
	// ErrNoCertificate indicates no certificate is available
	ErrNoCertificate = errors.New("no certificate available")
	// ErrInvalidProvider indicates an invalid or unsupported provider type
	ErrInvalidProvider = errors.New("invalid provider type")
	// ErrIdentityProofFailed indicates a failure to prove the identity of the device
	ErrIdentityProofFailed = errors.New("identity proof failed")
)

type Exportable struct {
	// Name of the identity
	name string
	// CSR defines the certificate signing request. The contents may vary depending on the type of the provider
	csr []byte
	// KeyPEM defines the private PEM bytes. The PEM block may vary depending on the type of the provider
	keyPEM []byte
}

// Name returns the name of the Exportable
func (e *Exportable) Name() string {
	return e.name
}

// CSR returns the CSR associated with the Exportable or an error if not initialized
func (e *Exportable) CSR() ([]byte, error) {
	if len(e.csr) == 0 {
		return nil, fmt.Errorf("CSR not initialized")
	}
	return e.csr, nil
}

// KeyPEM returns the PEM bytes associated with the Exportable or an error if not inialized
func (e *Exportable) KeyPEM() ([]byte, error) {
	if len(e.keyPEM) == 0 {
		return nil, fmt.Errorf("KeyPEM not initialized")
	}
	return e.keyPEM, nil
}

// ExportableProvider defines the interface for providing Exportable identities
type ExportableProvider interface {
	// NewExportable creates an Exportable for the specified name
	NewExportable(name string) (*Exportable, error)
}

// Provider defines the interface for identity providers that handle device authentication.
// Different implementations can support file-based keys, TPM-based keys, or other methods.
type Provider interface {
	// Initialize sets up the provider and prepares it for use
	Initialize(ctx context.Context) error
	// GetDeviceName returns the device name derived from the public key
	GetDeviceName() (string, error)
	// GenerateCSR creates a certificate signing request using this identity
	GenerateCSR(deviceName string) ([]byte, error)
	// ProveIdentity performs idempotent, provider-specific, identity verification.
	ProveIdentity(ctx context.Context, enrollmentRequest *v1beta1.EnrollmentRequest) error
	// StoreCertificate stores/persists the certificate received from enrollment.
	StoreCertificate(certPEM []byte) error
	// HasCertificate returns true if the provider has a certificate available
	HasCertificate() bool
	// CreateManagementClient creates a fully configured management client with this identity
	CreateManagementClient(config *base_client.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error)
	// CreateGRPCClient creates a fully configured gRPC client with this identity
	CreateGRPCClient(config *base_client.Config) (grpc_v1.RouterServiceClient, error)
	// WipeCredentials securely removes all stored credentials (certificates and keys)
	WipeCredentials() error
	// WipeCertificateOnly securely removes only the certificate (not keys or CSR)
	WipeCertificateOnly() error
}

// NewProvider creates an identity provider
func NewProvider(
	tpmClient tpm.Client,
	rw fileio.ReadWriter,
	config *agent_config.Config,
	log *log.PrefixLogger,
) Provider {
	if !config.ManagementService.Config.HasCredentials() {
		config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.GeneratedCertFile)
		config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.KeyFile)
	}

	clientCertPath := config.ManagementService.GetClientCertificatePath()
	clientKeyPath := config.ManagementService.GetClientKeyPath()
	clientCSRPath := GetCSRPath(config.DataDir)

	if tpmClient != nil {
		log.Info("Using TPM-based identity provider")
		return newTPMProvider(tpmClient, config, clientCertPath, clientCSRPath, rw, log)
	}

	log.Info("Using file-based identity provider")
	return newFileProvider(clientKeyPath, clientCertPath, clientCSRPath, rw, log)
}

// generateDeviceName creates a device name from a public key hash
func generateDeviceName(publicKey crypto.PublicKey) (string, error) {
	publicKeyHash, err := fccrypto.HashPublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to hash public key: %w", err)
	}
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash)), nil
}

// GetCSRPath returns the standard path where CSRs are stored
func GetCSRPath(dataDir string) string {
	return filepath.Join(dataDir, agent_config.DefaultCertsDirName, agent_config.CSRFile)
}

func StoreCSR(rw fileio.ReadWriter, csrPath string, csr []byte) error {
	if csr == nil {
		// Delete the CSR file if it exists
		exists, err := rw.PathExists(csrPath)
		if err != nil {
			return fmt.Errorf("checking CSR existence: %w", err)
		}
		if exists {
			if err := rw.OverwriteAndWipe(csrPath); err != nil {
				return fmt.Errorf("deleting CSR file: %w", err)
			}
		}
		return nil
	}
	return rw.WriteFile(csrPath, csr, 0600)
}

func LoadCSR(rw fileio.ReadWriter, csrPath string) ([]byte, bool, error) {
	exists, err := rw.PathExists(csrPath)
	if err != nil {
		return nil, false, fmt.Errorf("checking CSR existence: %w", err)
	}
	if !exists {
		return nil, false, nil
	}
	csr, err := rw.ReadFile(csrPath)
	if err != nil {
		return nil, false, fmt.Errorf("reading CSR: %w", err)
	}
	return csr, true, nil
}

func hasCertificate(rw fileio.ReadWriter, certPath string, log *log.PrefixLogger) bool {
	exists, err := rw.PathExists(certPath)
	if err != nil {
		log.Warnf("Failed to check certificate existence: %v", err)
		return false
	}
	return exists
}
