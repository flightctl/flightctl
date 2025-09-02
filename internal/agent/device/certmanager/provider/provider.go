package provider

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"slices"
)

// ProvisionerType represents the type identifier for a certificate provisioner.
// Use the provided constants instead of raw string literals.
type ProvisionerType string

const (
	ProvisionerTypeCSR        ProvisionerType = "csr"
	ProvisionerTypeSelfSigned ProvisionerType = "self-signed"
)

// StorageType represents the type identifier for a certificate storage backend.
// Use the provided constants instead of raw string literals.
type StorageType string

const (
	StorageTypeFilesystem StorageType = "filesystem"
)

// Logger provides a logging interface for certificate management operations.
// It supports different log levels for debugging, monitoring, and error reporting.
type Logger interface {
	Info(args ...interface{})
	Warn(args ...interface{})
	Error(args ...interface{})
	Debug(args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Debugf(format string, args ...interface{})
}

// ProvisionerProvider handles certificate provisioning operations.
// It provides methods to request and check certificate generation status,
// supporting both synchronous and asynchronous provisioning workflows.
type ProvisionerProvider interface {
	// Provision attempts to provision a certificate and returns:
	// - ready: true if certificate is ready, false if still processing
	// - cert: the X.509 certificate if ready
	// - key: the private key in PEM format if ready
	// - err: any error that occurred during provisioning
	Provision(ctx context.Context) (ready bool, cert *x509.Certificate, key []byte, err error)
}

// StorageProvider handles certificate storage operations.
// It provides methods to save, load, and delete certificates from storage backends.
type StorageProvider interface {
	// LoadCertificate loads a certificate from storage
	LoadCertificate(ctx context.Context) (*x509.Certificate, error)
	// Write stores a certificate and private key to storage
	Write(cert *x509.Certificate, keyPEM []byte) error
	// Delete removes a certificate from storage
	Delete(ctx context.Context) error
}

// ProvisionerFactory creates provisioner instances for a specific provisioner type.
// It provides validation and instantiation of provisioners based on certificate configuration.
type ProvisionerFactory interface {
	// Type returns the provisioner type identifier (e.g., "csr", "self-signed")
	Type() string
	// New creates a new provisioner instance from the certificate configuration
	New(log Logger, cc CertificateConfig) (ProvisionerProvider, error)
	// Validate checks if the certificate configuration is valid for this provisioner type
	Validate(log Logger, cc CertificateConfig) error
}

// StorageFactory creates storage instances for a specific storage type.
// It provides validation and instantiation of storage providers based on certificate configuration.
type StorageFactory interface {
	// Type returns the storage type identifier (e.g., "filesystem", "empty")
	Type() string
	// New creates a new storage instance from the certificate configuration
	New(log Logger, cc CertificateConfig) (StorageProvider, error)
	// Validate checks if the certificate configuration is valid for this storage type
	Validate(log Logger, cc CertificateConfig) error
}

// ConfigProvider supplies certificate configurations from various sources.
// It provides certificate configurations and optionally supports change notifications.
type ConfigProvider interface {
	// Name returns a unique identifier for this configuration provider
	Name() string
	// GetCertificateConfigs returns all certificate configurations from this provider
	GetCertificateConfigs() ([]CertificateConfig, error)
}

// CertificateConfig defines the complete configuration for a managed certificate.
// It includes provisioner settings, storage configuration, and renewal policies.
type CertificateConfig struct {
	// Unique certificate identifier
	Name string `json:"name"`
	// Provisioner configuration
	Provisioner ProvisionerConfig `json:"provisioner"`
	// Storage configuration
	Storage StorageConfig `json:"storage"`
}

// StorageConfig defines storage provider configuration including type and type-specific settings.
type StorageConfig struct {
	// Storage type identifier (e.g., "filesystem")
	Type StorageType `json:"type,omitempty"`
	// Type-specific configuration as JSON
	Config json.RawMessage `json:"config,omitempty"`
}

// ProvisionerConfig defines provisioner configuration including type and type-specific settings.
type ProvisionerConfig struct {
	// Provisioner type identifier (e.g., "csr", "self-signed")
	Type ProvisionerType `json:"type,omitempty"`
	// Type-specific configuration as JSON
	Config json.RawMessage `json:"config,omitempty"`
}

// Equal compares two CertificateConfig instances for equality.
// This is used to detect configuration changes that require certificate renewal.
func (c CertificateConfig) Equal(other CertificateConfig) bool {
	if c.Name != other.Name {
		return false
	}

	if !c.Provisioner.Equal(other.Provisioner) {
		return false
	}

	if !c.Storage.Equal(other.Storage) {
		return false
	}

	return true
}

// Equal compares two ProvisionerConfig instances for equality.
// This checks both the type and the type-specific configuration.
func (c ProvisionerConfig) Equal(other ProvisionerConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}

// Equal compares two StorageConfig instances for equality.
// This checks both the type and the type-specific configuration.
func (c StorageConfig) Equal(other StorageConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}
