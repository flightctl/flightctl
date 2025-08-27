package certmanager

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
)

const DefaultRequeueDelay = 10 * time.Second

// CertManager manages the complete certificate lifecycle for flight control agents.
// It coordinates certificate provisioning, storage, and cleanup across multiple configuration providers.
// The manager supports pluggable provisioners (CSR, self-signed, etc.) and storage
// backends (filesystem, etc.) through factory patterns.
type CertManager struct {
	log provider.Logger
	// In-memory certificate state with optional persistent backing
	certificates *certStorage
	// Configuration providers (agent-config, file, static)
	configs map[string]provider.ConfigProvider
	// Certificate provisioner factories (CSR, self-signed, empty)
	provisioners map[string]provider.ProvisionerFactory
	// Storage provider factories (filesystem, empty)
	storages map[string]provider.StorageFactory
	// Queue for async certificate processing with retry logic
	processingQueue *CertificateProcessingQueue
	// Delay before retrying failed certificate operations
	requeueDelay time.Duration
}

// ManagerOption defines a functional option for configuring CertManager during initialization.
type ManagerOption func(*CertManager) error

// WithRequeueDelay sets a custom requeue delay for certificate provisioning checks.
// This delay is used when a certificate provisioning operation is not yet complete
// and needs to be retried (e.g., waiting for CSR approval).
func WithRequeueDelay(delay time.Duration) ManagerOption {
	return func(cm *CertManager) error {
		if delay <= 0 {
			return fmt.Errorf("requeue delay must be positive")
		}
		cm.requeueDelay = delay
		return nil
	}
}

// WithConfigProvider adds a configuration provider to the manager.
// Configuration providers supply certificate configurations and can notify of changes.
// Multiple providers can be registered (e.g., agent-config, file-based, static).
func WithConfigProvider(config provider.ConfigProvider) ManagerOption {
	return func(cm *CertManager) error {
		if config == nil {
			return fmt.Errorf("provided config provider is nil")
		}

		name := config.Name()
		if _, ok := cm.configs[config.Name()]; ok {
			return fmt.Errorf("config provider with name %q already exists", name)
		}

		cm.configs[name] = config
		return nil
	}
}

// WithProvisionerProvider registers a provisioner factory with the manager.
// Provisioner factories create certificate provisioners (CSR, self-signed, etc.)
// based on certificate configuration. Each factory handles a specific provisioner type.
func WithProvisionerProvider(prov provider.ProvisionerFactory) ManagerOption {
	return func(cm *CertManager) error {
		if prov == nil {
			return fmt.Errorf("provided provisioner factory is nil")
		}

		t := prov.Type()
		if _, exists := cm.provisioners[t]; exists {
			return fmt.Errorf("provisioner factory for type %q already exists", t)
		}

		cm.provisioners[t] = prov
		return nil
	}
}

// WithStorageProvider registers a storage factory with the manager.
// Storage factories create certificate storage providers (filesystem, etc.) that
// handle writing certificates and private keys to their final destinations.
func WithStorageProvider(store provider.StorageFactory) ManagerOption {
	return func(cm *CertManager) error {
		if store == nil {
			return fmt.Errorf("provided storage factory is nil")
		}

		t := store.Type()
		if _, exists := cm.storages[t]; exists {
			return fmt.Errorf("storage factory for type %q already exists", t)
		}

		cm.storages[t] = store
		return nil
	}
}

// NewManager creates and initializes a new CertManager with the provided options.
func NewManager(ctx context.Context, log provider.Logger, opts ...ManagerOption) (*CertManager, error) {
	if log == nil {
		return nil, fmt.Errorf("logger is nil")
	}

	cm := &CertManager{
		log:          log,
		configs:      make(map[string]provider.ConfigProvider),
		provisioners: make(map[string]provider.ProvisionerFactory),
		storages:     make(map[string]provider.StorageFactory),
		certificates: newCertStorage(),
	}

	for _, opt := range opts {
		if optErr := opt(cm); optErr != nil {
			return nil, fmt.Errorf("failed to apply option: %w", optErr)
		}
	}

	if cm.requeueDelay == 0 {
		cm.requeueDelay = DefaultRequeueDelay
	}

	cm.processingQueue = NewCertificateProcessingQueue(cm.ensureCertificate)
	go cm.processingQueue.Run(ctx)
	return cm, nil
}

// Sync performs a full synchronization of all certificate providers.
func (cm *CertManager) Sync(ctx context.Context, _ *config.Config) error {
	cm.log.Debug("Starting certificate sync")
	if err := cm.sync(ctx); err != nil {
		cm.log.Errorf("certificate management sync failed: %v", err)
		return err
	}
	return nil
}

// sync performs a full synchronization of all certificate providers.
// It iterates through all registered configuration providers, syncs their certificates,
// and cleans up any providers that are no longer configured.
func (cm *CertManager) sync(ctx context.Context) error {
	handledProviders := make([]string, 0, len(cm.configs))

	defer func() {
		cleanupErr := cm.cleanupUntrackedProviders(handledProviders)
		if cleanupErr != nil {
			cm.log.Errorf("Failed to cleanup untracked providers: %v", cleanupErr)
		}
	}()

	for providerName, cfgProvider := range cm.configs {
		handledProviders = append(handledProviders, providerName)

		if err := cm.syncProvider(ctx, cfgProvider); err != nil {
			cm.log.Errorf("syncProvider failed for %q: %v", providerName, err)
		}
	}
	return nil
}

// syncProvider synchronizes certificates from a specific configuration provider.
// It loads certificate configurations, ensures each certificate is properly managed,
// and cleans up any certificates that are no longer configured.
func (cm *CertManager) syncProvider(ctx context.Context, provider provider.ConfigProvider) error {
	handledCertificates := make([]string, 0)
	providerName := provider.Name()

	defer func() {
		cleanupErr := cm.cleanupUntrackedCertificates(providerName, handledCertificates)
		if cleanupErr != nil {
			cm.log.Errorf("Failed to cleanup untracked certificates: %v", cleanupErr)
		}
	}()

	configs, loadErr := provider.GetCertificateConfigs()
	if loadErr != nil {
		// Mark existing certificates as handled so they won't be deleted
		cm.log.Errorf("failed to load certificate configs from provider %q: %v", providerName, loadErr)

		if _, ensureErr := cm.certificates.EnsureProvider(providerName); ensureErr != nil {
			return fmt.Errorf("ensure provider %q: %w", providerName, ensureErr)
		}

		certs, snapErr := cm.certificates.ReadCertificates(providerName)
		if snapErr != nil {
			// Be conservative: without a snapshot we might delete valid certs.
			return fmt.Errorf("snapshot existing certificates for provider %q: %w", providerName, snapErr)
		}

		for _, c := range certs {
			handledCertificates = append(handledCertificates, c.Name)
		}

		return fmt.Errorf("load certificate configs from provider %q: %w", providerName, loadErr)
	}

	if _, err := cm.certificates.EnsureProvider(providerName); err != nil {
		return err
	}

	for _, cfg := range configs {
		if err := cm.syncCertificate(ctx, provider, cfg); err != nil {
			cm.log.Errorf("syncCertificate failed for %q/%q: %v", providerName, cfg.Name, err)
		}
		handledCertificates = append(handledCertificates, cfg.Name)
	}

	return nil
}

// syncCertificate synchronizes a single certificate.
func (cm *CertManager) syncCertificate(ctx context.Context, provider provider.ConfigProvider, cfg provider.CertificateConfig) error {
	var err error
	providerName := provider.Name()
	certName := cfg.Name

	cert, err := cm.certificates.ReadCertificate(providerName, certName)
	if err != nil {
		cert = cm.createCertificate(ctx, provider, cfg)
	}

	cert.mu.Lock()
	defer cert.mu.Unlock()

	if cm.processingQueue.IsProcessing(providerName, cert.Name) {
		_, usedCfg := cm.processingQueue.Get(providerName, cert.Name)

		if !usedCfg.Equal(cfg) {
			// Remove old queued item
			cm.processingQueue.Remove(providerName, cert.Name)

			// Re-queue with new config
			if err := cm.provisionCertificate(ctx, providerName, cert, cfg); err != nil {
				return fmt.Errorf("failed to provision certificate %q from provider %q: %w", cert.Name, providerName, err)
			}
			cm.log.Debugf("Config changed during processing — re-queued provision for certificate %q of provider %q", certName, providerName)
		}
		return nil
	}

	if !cm.shouldprovisionCertificate(providerName, cert, cfg) {
		cert.Config = cfg
		cm.log.Debugf("Certificate %q for provider %q: no provision required", certName, providerName)
		return nil
	}

	if err := cm.provisionCertificate(ctx, providerName, cert, cfg); err != nil {
		return fmt.Errorf("failed to provision certificate %q from provider %q: %w", cert.Name, providerName, err)
	}

	cm.log.Debugf("Provision triggered for certificate %q of provider %q", certName, providerName)
	return nil
}

// createCertificate creates a new certificate object and attempts to load existing
// certificate information from the storage provider if available.
func (cm *CertManager) createCertificate(ctx context.Context, provider provider.ConfigProvider, cfg provider.CertificateConfig) *certificate {
	providerName := provider.Name()
	certName := cfg.Name

	cert := &certificate{
		Name:   certName,
		Config: cfg,
	}

	// Remove from processing queue if already in flight (resetting any previous state)
	if cm.processingQueue.IsProcessing(providerName, certName) {
		cm.processingQueue.Remove(providerName, certName)
	}

	// Try to load existing certificate details from storage provider
	storage, err := cm.initStorageProvider(cfg)
	if err == nil {
		parsedCert, loadErr := storage.LoadCertificate(ctx)
		if loadErr == nil && parsedCert != nil {
			cm.addCertificateInfo(cert, parsedCert)
		} else if loadErr != nil {
			cm.log.Debugf("no existing cert loaded for %q/%q: %v", providerName, certName, loadErr)
		}
	} else {
		cm.log.Errorf("failed to init storage provider for certificate %q from provider %q: %v", certName, providerName, err)
	}

	if err := cm.certificates.StoreCertificate(providerName, cert); err != nil {
		cm.log.Errorf("failed to store certificate %q from provider %q: %v", certName, providerName, err)
	}

	return cert
}

// shouldprovisionCertificate determines whether a certificate needs provisioning.
func (cm *CertManager) shouldprovisionCertificate(providerName string, cert *certificate, cfg provider.CertificateConfig) bool {
	// Missing critical cert info — first provision.
	if cert.Info.NotAfter == nil || cert.Info.NotBefore == nil {
		cm.log.Debugf("Certificate %q for provider %q: missing NotBefore/NotAfter — initial provisioning", cert.Name, providerName)
		return true
	}

	if !cert.Config.Provisioner.Equal(cfg.Provisioner) || !cert.Config.Storage.Equal(cfg.Storage) {
		cm.log.Debugf("Certificate %q for provider %q: provisioner or storage changed - needs provisioning", cert.Name, providerName)
		return true
	}

	return false
}

// provisionCertificate queues a certificate for provisioning by adding it to the processing queue.
func (cm *CertManager) provisionCertificate(_ context.Context, providerName string, cert *certificate, cfg provider.CertificateConfig) error {
	return cm.processingQueue.Process(providerName, cert, cfg)
}

// ensureCertificate is the main certificate processing function called by the processing queue.
func (cm *CertManager) ensureCertificate(ctx context.Context, providerName string, cert *certificate, cfg *provider.CertificateConfig) *time.Duration {
	cert.mu.Lock()
	defer cert.mu.Unlock()

	defer func() {
		// Always persist certificate state after execution
		if err := cm.certificates.StoreCertificate(providerName, cert); err != nil {
			cm.log.Errorf("failed to store certificate %q from provider %q: %v", cert.Name, providerName, err)
		}
	}()

	// Attempt to ensure certificate (provision)
	retryDelay, err := cm.ensureCertificate_do(ctx, providerName, cert, cfg)
	if err != nil {
		cm.log.Errorf("failed to ensure certificate %q from provider %q: %v", cert.Name, providerName, err)

		// On failure, reset provisioner and storage to force re-init next time
		cert.Provisioner = nil
		cert.Storage = nil
		return nil
	}

	// If no retry delay is returned, we consider it "final success"
	if retryDelay == nil {
		cert.Provisioner = nil
		cert.Storage = nil
	}

	return retryDelay
}

// ensureCertificate_do performs the actual certificate provisioning work.
// It initializes provisioner and storage providers, requests certificate provisioning,
// and writes the certificate to storage when ready.
func (cm *CertManager) ensureCertificate_do(ctx context.Context, providerName string, cert *certificate, cfg *provider.CertificateConfig) (*time.Duration, error) {
	if cfg == nil {
		return nil, fmt.Errorf("nil configurations")
	}

	config := *cfg
	certName := cert.Name

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	if cert.Storage == nil {
		s, err := cm.initStorageProvider(config)
		if err != nil {
			return nil, err
		}
		cert.Storage = s
	}

	if cert.Provisioner == nil {
		p, err := cm.initProvisionerProvider(config)
		if err != nil {
			return nil, err
		}
		cert.Provisioner = p
	}

	ready, crt, keyBytes, err := cert.Provisioner.Provision(ctx)
	if err != nil {
		return nil, err
	}

	if !ready {
		return &cm.requeueDelay, nil
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// check storage drift
	if !cert.Config.Storage.Equal(cfg.Storage) {
		cm.log.Debugf("Certificate %q for provider %q: storage configuration changed, deleting old storage", certName, providerName)
		if err := cm.purgeStorage(ctx, providerName, cert); err != nil {
			cm.log.Error(err.Error())
		}
	}

	if err := cert.Storage.Write(crt, keyBytes); err != nil {
		return nil, err
	}

	cm.addCertificateInfo(cert, crt)

	cert.Config = config
	cert.Provisioner = nil
	cert.Storage = nil
	return nil, nil
}

// addCertificateInfo extracts and stores certificate information from a parsed X.509 certificate.
func (cm *CertManager) addCertificateInfo(cert *certificate, parsedCert *x509.Certificate) {
	cert.Info.NotBefore = &parsedCert.NotBefore
	cert.Info.NotAfter = &parsedCert.NotAfter
}

// cleanupUntrackedProviders removes certificate providers that are no longer configured.
// It cancels any in-flight processing for certificates from removed providers.
func (cm *CertManager) cleanupUntrackedProviders(keepProviders []string) error {
	keepMap := make(map[string]struct{}, len(keepProviders))
	for _, name := range keepProviders {
		keepMap[name] = struct{}{}
	}

	providers, err := cm.certificates.ListProviderNames()
	if err != nil {
		return fmt.Errorf("failed to list provider names: %w", err)
	}

	for _, providerName := range providers {
		if _, ok := keepMap[providerName]; ok {
			continue
		}

		certs, err := cm.certificates.ReadCertificates(providerName)
		if err != nil {
			cm.log.Errorf("failed to read certificates for provider %q: %v", providerName, err)
			continue
		}

		for _, cert := range certs {
			if cm.processingQueue.IsProcessing(providerName, cert.Name) {
				cm.processingQueue.Remove(providerName, cert.Name)
			}
		}

		if err := cm.certificates.RemoveProvider(providerName); err != nil {
			cm.log.Errorf("failed to remove provider %q: %v", providerName, err)
			continue
		}

		cm.log.Debugf("Removed untracked provider %q and all associated certificates", providerName)
	}

	return nil
}

// cleanupUntrackedCertificates removes certificates that are no longer configured
// from a specific provider. It cancels any in-flight processing for removed certificates.
func (cm *CertManager) cleanupUntrackedCertificates(providerName string, keepCerts []string) error {
	if providerName == "" {
		return fmt.Errorf("provider name is empty")
	}

	keepMap := make(map[string]struct{}, len(keepCerts))
	for _, name := range keepCerts {
		keepMap[name] = struct{}{}
	}

	certs, err := cm.certificates.ReadCertificates(providerName)
	if err != nil {
		return fmt.Errorf("failed to read certificates for provider %q: %w", providerName, err)
	}

	for _, cert := range certs {
		if _, keep := keepMap[cert.Name]; keep {
			continue
		}

		if cm.processingQueue.IsProcessing(providerName, cert.Name) {
			cm.processingQueue.Remove(providerName, cert.Name)
		}

		if err := cm.certificates.RemoveCertificate(providerName, cert.Name); err != nil {
			cm.log.Errorf("failed to remove certificate %q from provider %q: %v", cert.Name, providerName, err)
			continue
		}

		cm.log.Debugf("Removed untracked certificate %q from provider %q", cert.Name, providerName)
	}

	return nil
}

// initProvisionerProvider creates a provisioner provider from the certificate configuration.
// It validates the configuration and returns a provisioner capable of generating certificates.
func (cm *CertManager) initProvisionerProvider(cfg provider.CertificateConfig) (provider.ProvisionerProvider, error) {
	if strings.TrimSpace(string(cfg.Provisioner.Type)) == "" {
		return nil, fmt.Errorf("provisioner type is not set for certificate %q", cfg.Name)
	}

	p, ok := cm.provisioners[string(cfg.Provisioner.Type)]
	if !ok {
		return nil, fmt.Errorf("provisioner type %q not registered", cfg.Provisioner.Type)
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, fmt.Errorf("validation failed for provisioner type %q: %w", cfg.Provisioner.Type, err)
	}

	return p.New(cm.log, cfg)
}

// initStorageProvider creates a storage provider from the certificate configuration.
// It validates the configuration and returns a storage provider capable of writing certificates.
func (cm *CertManager) initStorageProvider(cfg provider.CertificateConfig) (provider.StorageProvider, error) {
	if strings.TrimSpace(string(cfg.Storage.Type)) == "" {
		return nil, fmt.Errorf("storage type is not set for certificate %q", cfg.Name)
	}

	p, ok := cm.storages[string(cfg.Storage.Type)]
	if !ok {
		return nil, fmt.Errorf("storage type %q not registered", cfg.Storage.Type)
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, fmt.Errorf("validation failed for storage type %q: %w", cfg.Storage.Type, err)
	}

	return p.New(cm.log, cfg)
}

// purgeStorage removes certificate and key files from the storage provider.
func (cm *CertManager) purgeStorage(ctx context.Context, providerName string, cert *certificate) error {
	certName := cert.Name

	storage, err := cm.initStorageProvider(cert.Config)
	if err != nil {
		return fmt.Errorf("failed to initialize old storage provider for certificate %q from provider %q: %w", certName, providerName, err)
	}

	if err := storage.Delete(ctx); err != nil {
		return fmt.Errorf("failed to delete old storage for certificate %q from provider %q: %w", certName, providerName, err)
	}

	return nil
}
