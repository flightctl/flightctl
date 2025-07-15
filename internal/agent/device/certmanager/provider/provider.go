package provider

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"slices"

	"github.com/flightctl/flightctl/internal/util"
)

// Logger provides a logging interface for certificate management operations.
// It supports different log levels for debugging, monitoring, and error reporting.
type Logger interface {
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

// SupportsNotify is an optional interface for configuration providers that can notify
// about configuration changes. This enables reactive certificate management.
type SupportsNotify interface {
	// RegisterConfigChangeChannel registers a channel to receive configuration change notifications
	RegisterConfigChangeChannel(ch chan<- ConfigProvider, cp ConfigProvider) error
}

// ConfigProviderChangeNotifier provides common functionality for configuration providers
// that support change notifications. It manages the notification channel and provides
// a method to trigger configuration change events.
type ConfigProviderChangeNotifier struct {
	changeCh chan<- ConfigProvider // Channel to send configuration change notifications
	self     ConfigProvider        // Reference to the provider that owns this notifier
}

// RegisterConfigChangeChannel registers a channel to receive configuration change notifications.
// This is typically called during certificate manager initialization.
func (n *ConfigProviderChangeNotifier) RegisterConfigChangeChannel(ch chan<- ConfigProvider, cp ConfigProvider) error {
	n.self = cp
	n.changeCh = ch
	return nil
}

// TriggerConfigChange sends a configuration change notification to the registered channel.
// This should be called when the configuration provider detects a change in its configuration.
func (n *ConfigProviderChangeNotifier) TriggerConfigChange() {
	if n.changeCh != nil {
		select {
		case n.changeCh <- n.self:
		default:
		}
	}
}

// StateStorageProvider defines an interface for saving and loading certificate state.
// This enables persistence of certificate metadata and state across agent restarts.
type StateStorageProvider interface {
	// StoreState persists data by reading from the provided reader (e.g., JSON stream).
	StoreState(io.Reader) error

	// LoadState restores data by writing to the provided writer.
	LoadState(io.Writer) error
}

// CertificateConfig defines the complete configuration for a managed certificate.
// It includes provisioner settings, storage configuration, and renewal policies.
type CertificateConfig struct {
	Name             string            `json:"name"`                        // Unique certificate identifier
	Provisioner      ProvisionerConfig `json:"provisioner"`                 // Provisioner configuration
	Storage          StorageConfig     `json:"storage"`                     // Storage configuration
	AllowRenew       *bool             `json:"allow-renew,omitempty"`       // Whether automatic renewal is enabled
	RenewalThreshold *util.Duration    `json:"renewal-threshold,omitempty"` // Time before expiration to trigger renewal
}

// StorageConfig defines storage provider configuration including type and type-specific settings.
type StorageConfig struct {
	Type   string          `json:"type,omitempty"`   // Storage type identifier (e.g., "filesystem")
	Config json.RawMessage `json:"config,omitempty"` // Type-specific configuration as JSON
}

// ProvisionerConfig defines provisioner configuration including type and type-specific settings.
type ProvisionerConfig struct {
	Type   string          `json:"type,omitempty"`   // Provisioner type identifier (e.g., "csr", "self-signed")
	Config json.RawMessage `json:"config,omitempty"` // Type-specific configuration as JSON
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

	if (c.AllowRenew == nil) != (other.AllowRenew == nil) {
		return false
	}

	if c.AllowRenew != nil && *c.AllowRenew != *other.AllowRenew {
		return false
	}

	if (c.RenewalThreshold == nil) != (other.RenewalThreshold == nil) {
		return false
	}

	if c.RenewalThreshold != nil && *c.RenewalThreshold != *other.RenewalThreshold {
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
