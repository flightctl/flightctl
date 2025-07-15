package storage

import (
	"context"
	"crypto/x509"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// EmptyStorage is a placeholder storage provider that always fails storage operations.
// It's used for testing, development, or scenarios where certificates should not be persisted.
// All operations return errors indicating this is an empty storage provider.
type EmptyStorage struct {
}

// NewEmptyStorage creates a new empty storage instance.
// This storage provider is stateless and requires no configuration.
func NewEmptyStorage() *EmptyStorage {
	return &EmptyStorage{}
}

// LoadCertificate always fails with an error indicating this is an empty storage provider.
// This method never returns a certificate and is intended for testing or placeholder scenarios.
func (fs *EmptyStorage) LoadCertificate(ctx context.Context) (*x509.Certificate, error) {
	return nil, fmt.Errorf("empty storage")
}

// Write always fails with an error indicating this is an empty storage provider.
// This method never persists certificates and is intended for testing or placeholder scenarios.
func (fs *EmptyStorage) Write(cert *x509.Certificate, keyPEM []byte) error {
	return fmt.Errorf("empty storage")
}

// Delete always fails with an error indicating this is an empty storage provider.
// This method never deletes certificates and is intended for testing or placeholder scenarios.
func (fs *EmptyStorage) Delete(ctx context.Context) error {
	return fmt.Errorf("empty storage")
}

// EmptyStorageFactory implements StorageFactory for empty storage providers.
// It creates empty storage providers that always fail, useful for testing and development.
type EmptyStorageFactory struct {
}

// NewEmptyStorageFactory creates a new empty storage factory.
// This factory is stateless and requires no configuration.
func NewEmptyStorageFactory() *EmptyStorageFactory {
	return &EmptyStorageFactory{}
}

// Type returns the storage type string used as map key in the certificate manager.
func (f *EmptyStorageFactory) Type() string {
	return "empty"
}

// New creates a new EmptyStorage instance.
// The certificate configuration is ignored since empty storage providers have no configuration.
func (f *EmptyStorageFactory) New(log provider.Logger, cc provider.CertificateConfig) (provider.StorageProvider, error) {
	return NewEmptyStorage(), nil
}

// Validate always returns nil since empty storage providers have no configuration to validate.
// This allows empty storage providers to be used in any certificate configuration.
func (f *EmptyStorageFactory) Validate(log provider.Logger, cc provider.CertificateConfig) error {
	return nil
}
