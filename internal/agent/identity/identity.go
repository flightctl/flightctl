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
)

// Provider defines the interface for identity providers that handle device authentication.
// Different implementations can support file-based keys, TPM-based keys, or other methods.
type Provider interface {
	// Initialize sets up the provider and prepares it for use
	Initialize(ctx context.Context) error
	// GetDeviceName returns the device name derived from the public key
	GetDeviceName() (string, error)
	// GenerateCSR creates a certificate signing request using this identity
	GenerateCSR(deviceName string) ([]byte, error)
	// StoreCertificate stores/persists the certificate received from enrollment
	StoreCertificate(certPEM []byte) error
	// HasCertificate returns true if the provider has a certificate available
	HasCertificate() bool
	// CreateManagementClient creates a fully configured management client with this identity
	CreateManagementClient(config *base_client.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error)
	// CreateGRPCClient creates a fully configured gRPC client with this identity
	CreateGRPCClient(config *base_client.Config) (grpc_v1.RouterServiceClient, error)
	// WipeCredentials securely removes all stored credentials
	WipeCredentials() error
	// Close cleans up any resources used by the provider
	Close(ctx context.Context) error
}

// TPMProvider defines the interface for TPM-specific operations
// This allows identity providers to optionally expose TPM functionality
// without polluting the main identity provider interface
type TPMProvider interface {
	// GetEKCert returns the TPM Endorsement Key certificate (EK cert) if available
	GetEKCert() ([]byte, error)
	// GetTPMCertifyCert returns the TPM certify certificate that proves the LDevID was created by the TPM
	GetTPMCertifyCert() ([]byte, error)
	// GetTCGAttestation returns the complete TCG compliant attestation bundle
	GetTCGAttestation() (*tpm.AttestationBundle, error)
}

// TPMCapable is an optional interface that identity providers can implement
// to expose TPM functionality
type TPMCapable interface {
	// GetTPM returns the TPM provider if available, nil otherwise
	GetTPM() (TPMProvider, bool)
}

// NewProvider creates an identity provider
func NewProvider(
	tpmClient *tpm.Client,
	rw fileio.ReadWriter,
	config *agent_config.Config,
	log *log.PrefixLogger,
) Provider {
	if tpmClient != nil {
		log.Info("Using TPM-based identity provider")
		return newTPMProvider(tpmClient, log)
	}

	if !config.ManagementService.Config.HasCredentials() {
		config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.GeneratedCertFile)
		config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.KeyFile)
	}

	clientKeyPath := config.ManagementService.AuthInfo.ClientKey
	clientCertPath := config.ManagementService.GetClientCertificatePath()

	log.Info("Using file-based identity provider")
	return newFileProvider(clientKeyPath, clientCertPath, rw, log)
}

// generateDeviceName creates a device name from a public key hash
func generateDeviceName(publicKey crypto.PublicKey) (string, error) {
	publicKeyHash, err := fccrypto.HashPublicKey(publicKey)
	if err != nil {
		return "", fmt.Errorf("failed to hash public key: %w", err)
	}
	return strings.ToLower(base32.HexEncoding.WithPadding(base32.NoPadding).EncodeToString(publicKeyHash)), nil
}
