package common

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"io"
	"slices"

	"github.com/flightctl/flightctl/internal/util"
)

type Logger interface {
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type ProvisionerProvider interface {
	Provision(ctx context.Context) error
	Check(ctx context.Context) (bool, *x509.Certificate, []byte, error)
}

type StorageProvider interface {
	LoadCertificate(ctx context.Context) (*x509.Certificate, error)
	Write(cert *x509.Certificate, keyPEM []byte) error
	Delete(ctx context.Context) error
}

type ProvisionerFactory interface {
	Type() string
	New(log Logger, cc CertificateConfig) (ProvisionerProvider, error)
	Validate(log Logger, cc CertificateConfig) error
}

type StorageFactory interface {
	Type() string
	New(log Logger, cc CertificateConfig) (StorageProvider, error)
	Validate(log Logger, cc CertificateConfig) error
}

type ConfigProvider interface {
	Name() string
	GetCertificateConfigs() ([]CertificateConfig, error)
}

// Storage defines an interface for saving and loading certificate state.
type StateStorageProvider interface {
	// StoreState persists data by reading from the provided reader (e.g., JSON stream).
	StoreState(io.Reader) error

	// LoadState restores data by writing to the provided writer.
	LoadState(io.Writer) error
}

type CertificateConfig struct {
	Name             string            `json:"name"`
	Provisioner      ProvisionerConfig `json:"provisioner"`
	Storage          StorageConfig     `json:"storage"`
	RenewalThreshold util.Duration     `json:"renewal-threshold,omitempty"`
}

type StorageConfig struct {
	Type   string          `json:"type,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
}

type ProvisionerConfig struct {
	Type   string          `json:"type,omitempty"`
	Config json.RawMessage `json:"config,omitempty"`
}

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
	if c.RenewalThreshold != other.RenewalThreshold {
		return false
	}
	return true
}

func (c ProvisionerConfig) Equal(other ProvisionerConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}

func (c StorageConfig) Equal(other StorageConfig) bool {
	if c.Type != other.Type {
		return false
	}
	return slices.Equal(c.Config, other.Config)
}
