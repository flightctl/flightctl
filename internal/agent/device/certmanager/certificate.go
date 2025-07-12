package certmanager

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
)

type certificate struct {
	mu          sync.RWMutex               `json:"-"`
	Provisioner common.ProvisionerProvider `json:"-"`
	Storage     common.StorageProvider     `json:"-"`
	Name        string                     `json:"name"`
	Config      common.CertificateConfig   `json:"config"`
	Info        CertificateInfo            `json:"info,omitempty"`
}

type CertificateInfo struct {
	NotAfter     *time.Time `json:"notAfter,omitempty"`
	LastWritten  *time.Time `json:"lastWritten,omitempty"`
	Subject      *string    `json:"subject,omitempty"`
	SerialNumber *string    `json:"serialNumber,omitempty"`
	RenewalCount int        `json:"renewalCount"`
	Err          string     `json:"error"`
}

type certificateProvider struct {
	Name         string                  `json:"name"`
	Certificates map[string]*certificate `json:"certificates"`
	LastSyncedAt time.Time               `json:"lastSyncedAt"`
}

type certStorage struct {
	mu        sync.RWMutex
	state     common.StateStorageProvider
	providers map[string]*certificateProvider
}

func newCertStorage(state common.StateStorageProvider) (*certStorage, error) {
	ct := &certStorage{
		state:     state,
		providers: make(map[string]*certificateProvider),
	}
	return ct, ct.LoadState()
}

func (s *certStorage) LoadState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadState()
}

func (s *certStorage) StoreState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeState()
}

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
// If the provider does not exist, it creates a new one with the provided configs.
// If it already exists, it updates its configs and LastSyncedAt timestamp.
// Returns the existing or newly created provider.
func (s *certStorage) EnsureProvider(providerName string) (*certificateProvider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	provider, exists := s.providers[providerName]
	if !exists {
		provider = &certificateProvider{
			Name:         providerName,
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

func (s *certStorage) ListProviderNames() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	names := make([]string, 0, len(s.providers))
	for name := range s.providers {
		names = append(names, name)
	}

	return names, nil
}

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
