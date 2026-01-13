package certmanager

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"slices"
	"time"
)

// ProvisionerType represents the type identifier for a certificate provisioner.
// Use the provided constants instead of raw string literals.
type ProvisionerType string

// StorageType represents the type identifier for a certificate storage backend.
// Use the provided constants instead of raw string literals.
type StorageType string

// Logger provides a logging interface for certificate management operations.
// It supports different log levels for debugging, monitoring, and error reporting.
type Logger interface {
	Info(args ...any)
	Warn(args ...any)
	Error(args ...any)
	Debug(args ...any)
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
	Debugf(format string, args ...any)
}

// BundleProvider defines a capability boundary for certificate management.
//
// A bundle groups one or more configuration sources together with the provisioner
// and storage factories those configurations are allowed to reference.
//
// All CertificateConfig objects returned by the bundle's ConfigProviders must
// reference only provisioner and storage types that are present in the same bundle.
//
// Bundles are immutable by contract: implementations must return stable data
// for the lifetime of the bundle.
type BundleProvider interface {
	// Name returns the unique, stable identifier of the bundle.
	Name() string

	// Configs returns the configuration sources that belong to this bundle.
	Configs() map[string]ConfigProvider

	// Provisioners returns the provisioner factories allowed for this bundle,
	// keyed by provisioner type.
	Provisioners() map[string]ProvisionerFactory

	// Storages returns the storage factories allowed for this bundle,
	// keyed by storage type.
	Storages() map[string]StorageFactory

	// DisableRenewal disables time-based renewal for all certificates produced by
	// config providers in this bundle.
	DisableRenewal() bool
}

// ProvisionRequest describes a single provisioning attempt for a certificate.
//
// Desired is the target configuration we want to end up with.
//
// LastApplied is the previously applied configuration known to the manager at the time
// of this call.
//
// If LastApplied.IsEmpty(), the Provisioner MUST treat the previous state as unknown.
type ProvisionRequest struct {
	Desired     CertificateConfig
	LastApplied CertificateConfig

	// Attempt is the 1-based attempt number for the current provisioning cycle.
	// The manager increments this when retrying after Ready == false or transient errors.
	Attempt int
}

// ProvisionResult describes the outcome of a single provisioning attempt.
type ProvisionResult struct {
	// Ready indicates whether the certificate is fully provisioned.
	// If false, the caller should retry after RequeueAfter.
	Ready bool

	// Cert contains the provisioned X.509 certificate in PEM form.
	//
	// MUST be non-nil when Ready == true.
	// MUST be nil when Ready == false.
	Cert []byte

	// Key contains the private key material in PEM form, if exportable.
	//
	// Key MAY be nil even when Ready == true, to support non-exportable keys
	// (e.g. TPM-backed or HSM-backed keys).
	//
	// Storage providers MUST tolerate Key == nil.
	Key []byte

	// RequeueAfter specifies how long to wait before retrying provisioning
	// when Ready == false.
	//
	// If <= 0, the manager will apply DefaultRequeueDelay.
	RequeueAfter time.Duration

	// Meta carries optional, opaque data produced by the provisioner.
	//
	// This is intended for provisioner-to-storage handoff or other internal coordination.
	// Keys SHOULD be namespaced to avoid collisions.
	//
	// Values are opaque bytes and must be interpreted only by components that
	// explicitly understand them.
	//
	// Meta SHOULD be ignored when Ready == false.
	Meta map[string][]byte
}

// ProvisionerProvider handles certificate provisioning operations.
//
// Provisioners may be synchronous or asynchronous.
// They decide readiness and retry timing
// (e.g. exponential backoff, polling an external CA, waiting for approval).
type ProvisionerProvider interface {
	// Provision performs a single provisioning step.
	//
	// It MUST be safe to call multiple times.
	//
	// Contract:
	//   - If result.Ready == true:
	//       - result.Cert MUST be non-nil
	//       - result.Key MAY be nil (non-exportable key)
	//       - result.RequeueAfter is ignored
	//   - If result.Ready == false:
	//       - result.RequeueAfter controls retry timing (<=0 means "use manager default")
	//       - result.Cert, result.Key, and result.Meta are ignored.
	//
	// Returning a non-nil error indicates a hard failure and aborts the current provisioning attempt.
	// If err == nil, result MUST be non-nil.
	Provision(ctx context.Context, req ProvisionRequest) (*ProvisionResult, error)
}

// StoreRequest describes what should be persisted and where.
//
// Desired and LastApplied are the storage configurations for this certificate.
// If LastApplied.IsEmpty(), the storage MUST treat the previous state as unknown.
type StoreRequest struct {
	Result      *ProvisionResult
	Desired     StorageConfig
	LastApplied StorageConfig
}

// StorageProvider handles certificate storage operations.
//
// Storage implementations are responsible for persisting provisioned material
// and MAY perform best-effort cleanup or migration based on configuration changes.
//
// The certificate manager does NOT perform storage-level cleanup itself.
type StorageProvider interface {
	// Store persists provisioned material according to the desired configuration.
	//
	// The storage implementation MUST:
	//   - Persist req.Result.Cert.
	//   - Persist req.Result.Key if applicable for this storage backend.
	//
	// The storage implementation MAY:
	//   - Use req.LastApplied to perform best-effort cleanup or migration.
	//
	// If req.LastApplied.IsEmpty(), the previous state MUST be treated as unknown
	// and no cleanup or migration should be attempted.
	Store(ctx context.Context, req StoreRequest) error

	// LoadCertificate loads an existing certificate if present.
	//
	// Returning (nil, nil) indicates that no certificate is currently stored.
	LoadCertificate(ctx context.Context) (*x509.Certificate, error)
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
	// RenewBeforeExpiry controls when to renew relative to NotAfter.
	// If <= 0, the manager uses DefaultRenewBeforeExpiry.
	RenewBeforeExpiry time.Duration `json:"renew-before-expiry,omitempty"`
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

// Equal compares two CertificateConfig instances for semantic equality.
//
// Two configs are considered equal if they describe the same certificate intent:
//   - Same certificate name
//   - Same provisioner type and configuration
//   - Same storage type and configuration
//
// Note: Renewal policy fields (e.g. RenewBeforeExpiry) are intentionally ignored;
// they affect *when* to renew, not whether provisioner/storage intent changed.
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

// IsEmpty reports whether this CertificateConfig is the zero-value configuration.
func (c CertificateConfig) IsEmpty() bool {
	return c.Equal(CertificateConfig{})
}

// Equal compares two ProvisionerConfig instances for equality.
// This checks both the type and the type-specific configuration.
func (c ProvisionerConfig) Equal(other ProvisionerConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}

// IsEmpty reports whether this ProvisionerConfig is the zero-value configuration.
func (c ProvisionerConfig) IsEmpty() bool {
	return c.Equal(ProvisionerConfig{})
}

// Equal compares two StorageConfig instances for equality.
// This checks both the type and the type-specific configuration.
func (c StorageConfig) Equal(other StorageConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}

// IsEmpty reports whether this StorageConfig is the zero-value configuration.
func (c StorageConfig) IsEmpty() bool {
	return c.Equal(StorageConfig{})
}
