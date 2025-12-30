package certmanager

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// certificate represents a managed certificate with its configuration.
// It includes provisioner and storage providers for certificate lifecycle management.
// certificate is shared mutable state; callers must hold cert.mu when reading/writing its fields.
type certificate struct {
	// Mutex for thread-safe access to certificate fields
	mu sync.Mutex `json:"-"`
	// Provisioner instance for certificate generation
	Provisioner ProvisionerProvider `json:"-"`
	// Storage instance for certificate persistence
	Storage StorageProvider `json:"-"`
	// Certificate name/identifier
	Name string `json:"name"`
	// Certificate configuration including provisioner and storage settings
	Config CertificateConfig `json:"config"`
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

// certificateProvider groups certificates from a single (namespaced) configuration provider key.
type certificateProvider struct {
	// Map of certificate name to certificate object
	Certificates map[string]*certificate `json:"certificates"`
}

// certStorage manages in-memory certificate state.
// It provides thread-safe access to certificate data organized by provider key
type certStorage struct {
	mu        sync.RWMutex                    // Mutex for thread-safe access to storage
	providers map[string]*certificateProvider // Map of provider key -> provider data
}

// newCertStorage creates a new certificate storage.
func newCertStorage() *certStorage {
	return &certStorage{
		providers: make(map[string]*certificateProvider),
	}
}

// ReadCertificate retrieves a specific certificate by provider key and certificate name.
// certificate is shared mutable state; callers must hold cert.mu when reading/writing its fields.
// Returns an error if the provider key or certificate is not found.
func (s *certStorage) ReadCertificate(providerKey, certName string) (*certificate, error) {
	providerKey = strings.TrimSpace(providerKey)
	certName = strings.TrimSpace(certName)

	if providerKey == "" || certName == "" {
		return nil, fmt.Errorf("provider key or certificate name is empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.providers[providerKey]
	if !ok {
		return nil, fmt.Errorf("provider key %q not found", providerKey)
	}

	cert, ok := p.Certificates[certName]
	if !ok {
		return nil, fmt.Errorf("certificate %q not found in provider %q", certName, providerKey)
	}

	return cert, nil
}

// ReadCertificates retrieves all certificates for a specific provider key.
// certificate is shared mutable state; callers must hold cert.mu when reading/writing its fields.
// Returns an error if the provider key is not found.
func (s *certStorage) ReadCertificates(providerKey string) ([]*certificate, error) {
	providerKey = strings.TrimSpace(providerKey)

	if providerKey == "" {
		return nil, fmt.Errorf("provider key is empty")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	p, ok := s.providers[providerKey]
	if !ok {
		return nil, fmt.Errorf("provider key %q not found", providerKey)
	}

	certs := make([]*certificate, 0, len(p.Certificates))
	for _, cert := range p.Certificates {
		certs = append(certs, cert)
	}

	return certs, nil
}

// RemoveCertificate removes a specific certificate from storage.
// Returns an error if the provider key or certificate is not found.
func (s *certStorage) RemoveCertificate(providerKey, certName string) error {
	providerKey = strings.TrimSpace(providerKey)
	certName = strings.TrimSpace(certName)

	if providerKey == "" || certName == "" {
		return fmt.Errorf("provider key or certificate name is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.providers[providerKey]
	if !ok {
		return fmt.Errorf("provider key %q not found", providerKey)
	}

	if _, exists := p.Certificates[certName]; !exists {
		return fmt.Errorf("certificate %q not found in provider %q", certName, providerKey)
	}

	delete(p.Certificates, certName)
	return nil
}

// EnsureProvider ensures that a certificate provider with the given key exists in the storage.
// If the provider key does not exist, it creates a new one.
func (s *certStorage) EnsureProvider(providerKey string) error {
	providerKey = strings.TrimSpace(providerKey)

	if providerKey == "" {
		return fmt.Errorf("provider key is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.providers[providerKey]; !exists {
		s.providers[providerKey] = &certificateProvider{
			Certificates: make(map[string]*certificate),
		}
	}
	return nil
}

// RemoveProvider removes the provider with the given key from the storage, along with all its certificates.
func (s *certStorage) RemoveProvider(providerKey string) error {
	providerKey = strings.TrimSpace(providerKey)

	if providerKey == "" {
		return fmt.Errorf("provider key is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.providers[providerKey]; !exists {
		return fmt.Errorf("provider key %q not found", providerKey)
	}

	delete(s.providers, providerKey)
	return nil
}

// ListProviderKeys returns a list of all provider keys currently stored.
func (s *certStorage) ListProviderKeys() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys := make([]string, 0, len(s.providers))
	for key := range s.providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

// GetOrCreateCertificate returns the existing certificate instance if present.
// Otherwise it creates one via newCert, stores it, and returns it.
//
// newCert is invoked while holding certStorage's write lock.
// Therefore newCert MUST be cheap (no blocking), MUST NOT perform I/O, and MUST NOT
// call back into certStorage (directly or indirectly), to avoid deadlocks and
// long critical sections.
func (s *certStorage) GetOrCreateCertificate(providerKey, certName string, newCert func() *certificate) (*certificate, bool, error) {
	providerKey = strings.TrimSpace(providerKey)
	certName = strings.TrimSpace(certName)

	if providerKey == "" || certName == "" {
		return nil, false, fmt.Errorf("provider key or certificate name is empty")
	}

	if newCert == nil {
		return nil, false, fmt.Errorf("newCert is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.providers[providerKey]
	if !ok {
		return nil, false, fmt.Errorf("provider key %q not found", providerKey)
	}

	if c, ok := p.Certificates[certName]; ok {
		return c, false, nil
	}

	c := newCert()
	if c == nil {
		return nil, false, fmt.Errorf("newCert returned nil")
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = certName
	}
	if c.Name != certName {
		return nil, false, fmt.Errorf("newCert returned certificate with name %q (expected %q)", c.Name, certName)
	}

	p.Certificates[certName] = c
	return c, true, nil
}
