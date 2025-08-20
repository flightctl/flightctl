package certmanager

import (
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// certificate represents a managed certificate with its configuration.
// It includes provisioner and storage providers for certificate lifecycle management,
type certificate struct {
	// Mutex for thread-safe access to certificate fields
	mu sync.RWMutex `json:"-"`
	// Provisioner instance for certificate generation
	Provisioner provider.ProvisionerProvider `json:"-"`
	// Storage instance for certificate persistence
	Storage provider.StorageProvider `json:"-"`
	// Certificate name/identifier
	Name string `json:"name"`
	// Certificate configuration including provisioner and storage settings
	Config provider.CertificateConfig `json:"config"`
	// Certificate metadata and validity information
	Info CertificateInfo `json:"info,omitempty"`
}

// CertificateInfo contains parsed certificate metadata.
type CertificateInfo struct {
	// Certificate validity start time
	NotBefore *time.Time `json:"not_before,omitempty"`
	// Certificate validity end time (expiration)
	NotAfter *time.Time `json:"not_after,omitempty"`
}

// certificateProvider groups certificates from a single configuration provider
type certificateProvider struct {
	// Map of certificate name to certificate object
	Certificates map[string]*certificate `json:"certificates"`
}

// certStorage manages in-memory certificate state.
// It provides thread-safe access to certificate data organized by provider.
type certStorage struct {
	mu        sync.RWMutex                    // Mutex for thread-safe access to storage
	providers map[string]*certificateProvider // Map of provider name to provider data
}

// newCertStorage creates a new certificate storage.
func newCertStorage() *certStorage {
	return &certStorage{
		providers: make(map[string]*certificateProvider),
	}
}

// ReadCertificate retrieves a specific certificate by provider and certificate name.
// Returns an error if the provider or certificate is not found.
func (s *certStorage) ReadCertificate(providerName, certName string) (*certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if providerName == "" || certName == "" {
		return nil, fmt.Errorf("provider name or certificate name is empty")
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	cert, ok := provider.Certificates[certName]
	if !ok {
		return nil, fmt.Errorf("certificate %q not found in provider %q", certName, providerName)
	}

	return cert, nil
}

// ReadCertificates retrieves all certificates for a specific provider.
// Returns an error if the provider is not found.
func (s *certStorage) ReadCertificates(providerName string) ([]*certificate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if providerName == "" {
		return nil, fmt.Errorf("provider name is empty")
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider %q not found", providerName)
	}

	certs := make([]*certificate, 0, len(provider.Certificates))
	for _, cert := range provider.Certificates {
		certs = append(certs, cert)
	}

	return certs, nil
}

// RemoveCertificate removes a specific certificate from storage.
// Returns an error if the provider or certificate is not found.
func (s *certStorage) RemoveCertificate(providerName, certName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if providerName == "" || certName == "" {
		return fmt.Errorf("provider name or certificate name is empty")
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found", providerName)
	}

	if _, exists := provider.Certificates[certName]; !exists {
		return fmt.Errorf("certificate %q not found in provider %q", certName, providerName)
	}

	delete(provider.Certificates, certName)
	return nil
}

// StoreCertificate stores or updates a certificate in the storage.
func (s *certStorage) StoreCertificate(providerName string, cert *certificate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cert == nil {
		return fmt.Errorf("certificate is nil")
	}

	if cert.Name == "" {
		return fmt.Errorf("certificate name is empty")
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found", providerName)
	}

	provider.Certificates[cert.Name] = cert
	return nil
}

// EnsureProvider ensures that a certificate provider with the given name exists in the storage.
// If the provider does not exist, it creates a new one.
// Returns the existing or newly created provider.
func (s *certStorage) EnsureProvider(providerName string) (*certificateProvider, error) {
	if providerName == "" {
		return nil, fmt.Errorf("provider name is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	provider, exists := s.providers[providerName]
	if !exists {
		provider = &certificateProvider{
			Certificates: make(map[string]*certificate),
		}
		s.providers[providerName] = provider
	}
	return provider, nil
}

// RemoveProvider removes the provider with the given name from the storage, along with all its certificates.
func (s *certStorage) RemoveProvider(providerName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if providerName == "" {
		return fmt.Errorf("provider name is empty")
	}

	if _, exists := s.providers[providerName]; !exists {
		return fmt.Errorf("provider %q not found", providerName)
	}

	delete(s.providers, providerName)
	return nil
}

// ListProviderNames returns a list of all provider names currently stored.
func (s *certStorage) ListProviderNames() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}

	return names, nil
}
