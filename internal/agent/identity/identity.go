package identity

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	_ "embed"
	"encoding/base32"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	base_client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	// Environment variable that contains the path to the TPM storage password file
	TPMStoragePasswordFileEnv = "TPM_STORAGE_PASSWORD_FILE" // #nosec G101 - env var name, not a credential
	// Environment variable that contains the path to the TPM child service password file
	TPMChildPasswordFileEnv = "TPM_CHILD_PASSWORD_FILE" // #nosec G101 - env var name, not a credential
	// Environment variable for systemd credentials directory
	CredentialsDirEnv = "CREDENTIALS_DIRECTORY"
	// Secure credential path prefixes
	SecureCredentialsPrefix = "/run/credentials/"
	SharedMemoryPrefix      = "/dev/shm"
	// ChildCredentialDir is where child service sealed credentials are stored
	ChildCredentialDir = "/etc/flightctl/credentials/children" // #nosec G101 - directory path
	// ParentCredentialPath is the path for parent hierarchy password credential
	ParentCredentialPath = "/etc/flightctl/credentials/tpm-storage-password.sealed" // #nosec G101 - file path, not a credential
	// TPMStorageCredentialName is the credential name for systemd
	TPMStorageCredentialName = "tpm-storage-password" // #nosec G101 - credential name, not a credential
	// DefaultSecretLength is the default password length in bytes
	DefaultSecretLength = 32
)

// SealKeyType represents the type of key used for sealing credentials
type SealKeyType string

const (
	SealKeyHost     SealKeyType = "host"
	SealKeyTPM2     SealKeyType = "tpm2"
	SealKeyHostTPM2 SealKeyType = "host+tpm2"
)

// ServiceType represents whether a service is parent or child
type ServiceType string

const (
	ParentService ServiceType = "parent"
	ChildService  ServiceType = "child"
)

func (s SealKeyType) String() string {
	return string(s)
}

var (
	// ErrNotInitialized indicates the provider has not been initialized
	ErrNotInitialized = errors.New("identity provider not initialized")
	// ErrNoCertificate indicates no certificate is available
	ErrNoCertificate = errors.New("no certificate available")
	// ErrInvalidProvider indicates an invalid or unsupported provider type
	ErrInvalidProvider = errors.New("invalid provider type")
	// ErrIdentityProofFailed indicates a failure to prove the identity of the device
	ErrIdentityProofFailed = errors.New("identity proof failed")
	// ErrNoCredentials indicates systemd credentials are not configured
	ErrNoCredentials = errors.New("systemd credentials not configured")
	// ErrCredentialNotFound indicates the specific credential was not found
	ErrCredentialNotFound = errors.New("credential not found")
	// ErrInvalidCredential indicates the credential is malformed or corrupted
	ErrInvalidCredential = errors.New("invalid credential format")
	// ErrSystemdCredsNotAvailable indicates systemd-creds is not available or version is too old (need >= 250)
	ErrSystemdCredsNotAvailable = errors.New("systemd-creds not available or version < 250")
	// ErrTPM2NotAvailable indicates TPM2 support is not available in systemd-creds
	ErrTPM2NotAvailable = errors.New("TPM2 support not available")
	// ErrSealingFailed indicates the sealing operation failed
	ErrSealingFailed = errors.New("failed to seal password")
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
	ProveIdentity(ctx context.Context, enrollmentRequest *v1alpha1.EnrollmentRequest) error
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
	// Close cleans up any resources used by the provider
	Close(ctx context.Context) error
}

// NewProvider creates an identity provider
func NewProvider(
	tpmClient tpm.Client,
	rw fileio.ReadWriter,
	exec executer.Executer,
	config *agent_config.Config,
	log *log.PrefixLogger,
) Provider {
	if !config.ManagementService.Config.HasCredentials() {
		config.ManagementService.Config.AuthInfo.ClientCertificate = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.GeneratedCertFile)
		config.ManagementService.Config.AuthInfo.ClientKey = filepath.Join(config.DataDir, agent_config.DefaultCertsDirName, agent_config.KeyFile)
	}

	clientCertPath := config.ManagementService.GetClientCertificatePath()
	clientKeyPath := config.ManagementService.GetClientKeyPath()

	if tpmClient != nil {
		log.Info("Using TPM-based identity provider")
		return newTPMProvider(tpmClient, config, clientCertPath, rw, exec, log)
	}

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

// GetCredentialPassword retrieves the TPM storage password from systemd credentials
func GetCredentialPassword(rw fileio.ReadWriter, log *log.PrefixLogger) ([]byte, error) {
	credPath := os.Getenv(TPMStoragePasswordFileEnv)
	if credPath == "" {
		log.Errorf("Environment variable %s not set", TPMStoragePasswordFileEnv)
		return nil, fmt.Errorf("%w: %s not set", ErrNoCredentials, TPMStoragePasswordFileEnv)
	}
	log.Debugf("%s=%s", TPMStoragePasswordFileEnv, credPath)

	credPath = expandCredentialPath(credPath, log)
	log.Debugf("Expanded credential path: %s", credPath)

	if err := validateCredentialPath(credPath, log); err != nil {
		return nil, fmt.Errorf("invalid credential path: %w", err)
	}

	password, err := rw.ReadFile(credPath)
	if err != nil {
		exists, _ := rw.PathExists(credPath)
		if !exists {
			log.Errorf("Credential file does not exist: %s", credPath)
			return nil, fmt.Errorf("%w: %s", ErrCredentialNotFound, credPath)
		}
		log.Errorf("Failed to read credential file: %v", err)
		return nil, fmt.Errorf("failed to read credential: %w", err)
	}

	if len(password) == 0 {
		log.Error("Credential file is empty")
		return nil, ErrInvalidCredential
	}

	log.Debugf("Successfully read TPM password from credential (length=%d bytes)", len(password))
	return password, nil
}

// ValidateCredentialSetup checks if credentials are properly configured
func ValidateCredentialSetup(rw fileio.ReadWriter, log *log.PrefixLogger) error {
	credPath := os.Getenv(TPMStoragePasswordFileEnv)
	if credPath == "" {
		return fmt.Errorf("%w: %s environment variable not set", ErrNoCredentials, TPMStoragePasswordFileEnv)
	}

	credPath = expandCredentialPath(credPath, log)
	if err := validateCredentialPath(credPath, log); err != nil {
		return fmt.Errorf("credential path validation failed: %w", err)
	}

	exists, err := rw.PathExists(credPath)
	if err != nil {
		return fmt.Errorf("cannot check credential file: %w", err)
	}

	if !exists {
		return fmt.Errorf("%w: file does not exist at %s", ErrCredentialNotFound, credPath)
	}

	return nil
}

// checkParentCredentialAvailable checks if the parent TPM storage password credential exists
// Returns true if the credential is available, false if not found,
// or an error if there was a problem checking
func checkParentCredentialAvailable(rw fileio.ReadWriter, log *log.PrefixLogger) (bool, error) {
	credPath := os.Getenv(TPMStoragePasswordFileEnv)
	if credPath == "" {
		return false, nil
	}

	credPath = expandCredentialPath(credPath, log)
	exists, err := rw.PathExists(credPath)
	if err != nil {
		return false, fmt.Errorf("failed to check credential path: %w", err)
	}

	return exists, nil
}

// expandCredentialPath expands the systemd credential placeholder %d to CREDENTIALS_DIRECTORY.
// This follows the systemd.exec(5) convention where %d expands to the credentials directory.
func expandCredentialPath(path string, log *log.PrefixLogger) string {
	if !strings.HasPrefix(path, "%d/") && !strings.Contains(path, "/%d/") {
		return path
	}

	credDir := os.Getenv(CredentialsDirEnv)
	if credDir == "" {
		log.Warn("CREDENTIALS_DIRECTORY not set, cannot expand placeholder in path")
		path = strings.ReplaceAll(path, "%d/", "")
		path = strings.ReplaceAll(path, "/%d/", "/")
		return path
	}

	return strings.ReplaceAll(path, "%d", credDir)
}

// validateCredentialPath ensures the credential path is secure and prevents traversal attacks
func validateCredentialPath(path string, log *log.PrefixLogger) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("credential path must be absolute: %s", path)
	}

	if !isUnderAllowedPrefix(path) {
		return fmt.Errorf("credential path must be under %s or %s: %s", SecureCredentialsPrefix, SharedMemoryPrefix, path)
	}

	// resolve symlinks on the full path to prevent traversal attacks
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		// ff the file doesn't exist yet, check the parent directory
		if os.IsNotExist(err) {
			parentDir := filepath.Dir(path)
			resolvedDir, err := filepath.EvalSymlinks(parentDir)
			if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("cannot resolve credential directory: %w", err)
			}
			if err == nil {
				if !isUnderAllowedPrefix(resolvedDir) {
					return fmt.Errorf("resolved credential directory escapes allowed prefix: %s -> %s", parentDir, resolvedDir)
				}
			}
			return nil
		}
		return fmt.Errorf("cannot resolve credential path: %w", err)
	}

	if !isUnderAllowedPrefix(resolvedPath) {
		return fmt.Errorf("resolved credential path escapes allowed prefix: %s -> %s", path, resolvedPath)
	}

	log.Debugf("Credential path validated: %s (resolved: %s)", path, resolvedPath)
	return nil
}

// isUnderAllowedPrefix checks if a path is under SecureCredentialsPrefix or SharedMemoryPrefix
func isUnderAllowedPrefix(path string) bool {
	return strings.HasPrefix(path, SecureCredentialsPrefix) || strings.HasPrefix(path, SharedMemoryPrefix)
}

// CreateParentCredential creates and seals the parent (flightctl-agent) hierarchy password
// This password will be used to authorize TPM operations for child services
func CreateParentCredential(ctx context.Context, rw fileio.ReadWriter, exec executer.Executer, log *log.PrefixLogger) error {
	sealer, err := NewSealer(log, rw, exec)
	if err != nil {
		return fmt.Errorf("failed to create password sealer: %w", err)
	}

	if err := rw.MkdirAll("/etc/credstore", 0700); err != nil {
		return fmt.Errorf("failed to create credstore directory: %w", err)
	}

	password, err := GeneratePassword(DefaultSecretLength)
	if err != nil {
		return fmt.Errorf("failed to generate password: %w", err)
	}

	err = sealer.Seal(ctx, "flightctl-agent", SealKeyHost, password)
	if err != nil {
		fccrypto.SecureMemoryWipe(password)
		return fmt.Errorf("failed to seal parent credential: %w", err)
	}

	fccrypto.SecureMemoryWipe(password)

	log.Info("Successfully created parent TPM credential")
	return nil
}

// CreateChildCredential creates and seals a credential for a child service
// Child services get their own passwords that can be used with the parent's hierarchy password
func CreateChildCredential(ctx context.Context, childName string, rw fileio.ReadWriter, exec executer.Executer, log *log.PrefixLogger) error {
	childName = strings.TrimSuffix(childName, ".service")
	if err := rw.MkdirAll(ChildCredentialDir, 0700); err != nil {
		return fmt.Errorf("failed to create child credential directory: %w", err)
	}

	sealer, err := NewSealer(log, rw, exec)
	if err != nil {
		return fmt.Errorf("failed to create password sealer: %w", err)
	}

	log.Infof("Creating child credential for %s with seal key: %s", childName, SealKeyHostTPM2)

	password, err := GeneratePassword(DefaultSecretLength)
	if err != nil {
		return fmt.Errorf("failed to generate password for %s: %w", childName, err)
	}

	err = sealer.Seal(ctx, childName, SealKeyHostTPM2, password)
	if err != nil {
		fccrypto.SecureMemoryWipe(password)
		return fmt.Errorf("failed to seal child credential for %s: %w", childName, err)
	}

	defer fccrypto.SecureMemoryWipe(password)

	// create systemd drop-in to load the credential
	if err := createSystemdDropIn(ctx, ChildService, childName, rw, exec, log); err != nil {
		// Clean up sealed file on error
		sealedPath := filepath.Join(ChildCredentialDir, fmt.Sprintf("%s.sealed", childName))
		_ = rw.RemoveFile(sealedPath)
		return fmt.Errorf("failed to create systemd drop-in for %s: %w", childName, err)
	}

	log.Infof("Successfully created child credential for %s", childName)
	return nil
}

//go:embed fixtures/credential.conf
var credentialTemplate string

// GeneratePassword generates a new random password suitable for TPM storage hierarchy
func GeneratePassword(length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("password length must be greater than 0")
	}

	password := make([]byte, length)
	if _, err := rand.Read(password); err != nil {
		return nil, fmt.Errorf("failed to generate random password: %w", err)
	}

	return password, nil
}

// createSystemdDropIn creates a systemd drop-in configuration for loading sealed credentials
func createSystemdDropIn(ctx context.Context, serviceType ServiceType, serviceName string, rw fileio.ReadWriter, exec executer.Executer, log *log.PrefixLogger) error {
	var (
		dropinDir      string
		dropinFile     string
		credentialName string
		envVarName     string
	)

	if serviceName == "" {
		return fmt.Errorf("service name is required")
	}

	var sealedPath string
	switch serviceType {
	case ParentService:
		dropinDir = fmt.Sprintf("/etc/systemd/system/%s.service.d", serviceName)
		dropinFile = "10-tpm-credential.conf"
		credentialName = TPMStorageCredentialName
		sealedPath = ParentCredentialPath
		envVarName = TPMStoragePasswordFileEnv
	case ChildService:
		dropinDir = fmt.Sprintf("/etc/systemd/system/%s.d", serviceName)
		dropinFile = "20-tpm-auth.conf"
		credentialName = fmt.Sprintf("%s-password", serviceName)
		sealedPath = filepath.Join(ChildCredentialDir, fmt.Sprintf("%s.sealed", serviceName))
		envVarName = TPMChildPasswordFileEnv
	default:
		return fmt.Errorf("unknown service type: %s", serviceType)
	}

	dropinPath := filepath.Join(dropinDir, dropinFile)

	if serviceType == ParentService {
		exists, err := rw.PathExists(dropinPath)
		if err != nil {
			return fmt.Errorf("checking drop-in file: %w", err)
		}
		if exists {
			log.Debug("Drop-in file already exists, skipping")
			return nil
		}
	}

	if err := rw.MkdirAll(dropinDir, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating drop-in directory: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("dropin").Parse(credentialTemplate)
	if err != nil {
		return fmt.Errorf("parsing drop-in template: %w", err)
	}

	var buf bytes.Buffer
	templateData := struct {
		ServiceName    string
		CredentialName string
		SealedPath     string
		EnvVarName     string
	}{
		ServiceName:    serviceName,
		CredentialName: credentialName,
		SealedPath:     sealedPath,
		EnvVarName:     envVarName,
	}

	if err := tmpl.Execute(&buf, templateData); err != nil {
		return fmt.Errorf("executing drop-in template: %w", err)
	}

	if err := rw.WriteFile(dropinPath, buf.Bytes(), fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing drop-in file: %w", err)
	}

	log.Infof("Created systemd drop-in at %s", dropinPath)

	// reload systemd to pick up the new drop-in
	systemdClient := client.NewSystemd(exec)
	if err := systemdClient.DaemonReload(ctx); err != nil {
		return fmt.Errorf("reloading systemd daemon: %w", err)
	}

	log.Infof("Systemd daemon reloaded successfully")
	return nil
}
