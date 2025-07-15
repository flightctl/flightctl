package certmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

// certificate represents a managed certificate with its configuration, state, and metadata.
// It includes provisioner and storage providers for certificate lifecycle management,
// retry tracking, and detailed certificate information for renewal decisions.
type certificate struct {
	mu            sync.RWMutex                 `json:"-"`              // Mutex for thread-safe access to certificate fields
	Provisioner   provider.ProvisionerProvider `json:"-"`              // Provisioner instance for certificate generation (not persisted)
	Storage       provider.StorageProvider     `json:"-"`              // Storage instance for certificate persistence (not persisted)
	Name          string                       `json:"name"`           // Certificate name/identifier
	Config        provider.CertificateConfig   `json:"config"`         // Certificate configuration including provisioner and storage settings
	Info          CertificateInfo              `json:"info,omitempty"` // Certificate metadata and validity information
	RetryFailures int                          `json:"retry_failures"` // Number of consecutive provisioning failures
	Err           string                       `json:"error"`          // Last error message from failed provisioning attempts
}

// CertificateInfo contains parsed certificate metadata used for renewal decisions
// and monitoring certificate lifecycle status.
type CertificateInfo struct {
	LastProvisioned *time.Time `json:"last_provisioned,omitempty"` // Timestamp of last successful provisioning
	NotBefore       *time.Time `json:"not_before,omitempty"`       // Certificate validity start time
	NotAfter        *time.Time `json:"not_after,omitempty"`        // Certificate validity end time (expiration)
	CommonName      *string    `json:"common_name,omitempty"`      // Subject common name from certificate
	SerialNumber    *string    `json:"serial_number,omitempty"`    // Certificate serial number
	RenewalCount    int        `json:"renewal_count"`              // Number of times certificate has been renewed
}

// certificateProvider groups certificates from a single configuration provider
// with tracking of last synchronization time for cleanup purposes.
type certificateProvider struct {
	Certificates map[string]*certificate `json:"certificates"`   // Map of certificate name to certificate object
	LastSyncedAt time.Time               `json:"last_synced_at"` // Last time this provider was synchronized
}

// certStorage manages in-memory certificate state with optional persistent backing.
// It provides thread-safe access to certificate data organized by provider.
type certStorage struct {
	mu        sync.RWMutex                    // Mutex for thread-safe access to storage
	state     provider.StateStorageProvider   // Optional persistent storage backend
	providers map[string]*certificateProvider // Map of provider name to provider data
}

// newCertStorage creates a new certificate storage instance with optional state persistence.
// If a state storage provider is provided, it attempts to load existing state on creation.
func newCertStorage(state provider.StateStorageProvider) (*certStorage, error) {
	ct := &certStorage{
		state:     state,
		providers: make(map[string]*certificateProvider),
	}
	return ct, ct.LoadState()
}

// LoadState loads certificate state from the persistent storage backend if configured.
// This is called during storage initialization to restore previous state.
func (s *certStorage) LoadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadState()
}

// StoreState persists current certificate state to the storage backend if configured.
// This is called after state changes to maintain persistence across restarts.
func (s *certStorage) StoreState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeState()
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

// RemoveCertificate removes a specific certificate from storage and persists the change.
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

	// Save new state
	if err := s.storeState(); err != nil {
		return fmt.Errorf("failed to store state after certificate removal: %w", err)
	}

	return nil
}

// StoreCertificate stores or updates a certificate in the storage and persists the change.
// It updates the provider's last synced timestamp and saves state if persistence is enabled.
func (s *certStorage) StoreCertificate(providerName string, cert *certificate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if cert == nil {
		return fmt.Errorf("certificate is nil")
	}

	provider, ok := s.providers[providerName]
	if !ok {
		return fmt.Errorf("provider %q not found", providerName)
	}

	provider.Certificates[cert.Name] = cert
	provider.LastSyncedAt = time.Now()

	if err := s.storeState(); err != nil {
		return fmt.Errorf("failed to store state after certificate store: %w", err)
	}

	return nil
}

// EnsureProvider ensures that a certificate provider with the given name exists in the storage.
// If the provider does not exist, it creates a new one.
// If it already exists, it updates its LastSyncedAt timestamp.
// Returns the existing or newly created provider.
func (s *certStorage) EnsureProvider(providerName string) (*certificateProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	provider, exists := s.providers[providerName]
	if !exists {
		provider = &certificateProvider{
			Certificates: make(map[string]*certificate),
			LastSyncedAt: time.Now(),
		}
		s.providers[providerName] = provider
	} else {
		provider.LastSyncedAt = time.Now()
	}

	if err := s.storeState(); err != nil {
		return nil, fmt.Errorf("failed to store state after ensuring provider: %w", err)
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

	if err := s.storeState(); err != nil {
		return fmt.Errorf("failed to store state after removing provider: %w", err)
	}

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

// loadState loads certificate state from the persistent storage backend.
// This is an internal method called with the storage mutex already held.
func (s *certStorage) loadState() error {
	if s.state == nil {
		return nil // No backing state, nothing to do
	}

	var buf bytes.Buffer

	if err := s.state.LoadState(&buf); err != nil {
		return fmt.Errorf("failed to load state: %w", err)
	}

	// If state is empty (e.g., file doesn't exist or is empty), nothing to decode
	if buf.Len() == 0 {
		return nil
	}

	var snapshot map[string]*certificateProvider
	if err := json.NewDecoder(&buf).Decode(&snapshot); err != nil {
		return fmt.Errorf("failed to decode state: %w", err)
	}

	s.providers = snapshot
	return nil
}

// storeState persists current certificate state to the persistent storage backend.
// This is an internal method called with the storage mutex already held.
func (s *certStorage) storeState() error {
	if s.state == nil {
		return nil // No backing state, nothing to do
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(s.providers); err != nil {
		return fmt.Errorf("failed to encode state: %w", err)
	}

	if err := s.state.StoreState(&buf); err != nil {
		return fmt.Errorf("failed to store state: %w", err)
	}

	return nil
}
