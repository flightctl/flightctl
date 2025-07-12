package certmanager

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/agent/device/certmanager/common"
	"github.com/flightctl/flightctl/pkg/log"
)

const DefaultSyncInterval = 5 * time.Minute

// CertManager manages certificate lifecycle, including provisioners, storages, configs, renewal logic, and retry handling.
type CertManager struct {
	log             *log.PrefixLogger
	certificates    *certStorage
	configs         map[string]common.ConfigProvider
	provisioners    map[string]common.ProvisionerFactory
	storages        map[string]common.StorageFactory
	processingQueue *CertificateProcessingQueue
	syncInterval    time.Duration
}

type ManagerOption func(*CertManager) error

// WithSyncInterval sets a custom sync interval for the CertManager Run loop.
func WithSyncInterval(interval time.Duration) ManagerOption {
	return func(cm *CertManager) error {
		if interval <= 0 {
			return fmt.Errorf("sync interval must be positive")
		}
		cm.syncInterval = interval
		return nil
	}
}

// WithStateStorageProvider sets the state storage provider for certificate state persistence.
func WithStateStorageProvider(storage common.StateStorageProvider) ManagerOption {
	return func(cm *CertManager) error {
		if storage == nil {
			return fmt.Errorf("provided state storage provider is nil")
		}

		newCertStorage, err := newCertStorage(storage)
		if err != nil {
			return fmt.Errorf("failed to create cert storage: %w", err)
		}

		cm.certificates = newCertStorage
		return nil
	}
}

// WithConfigProvider adds a configuration provider to the manager.
func WithConfigProvider(config common.ConfigProvider) ManagerOption {
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
func WithProvisionerProvider(prov common.ProvisionerFactory) ManagerOption {
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
func WithStorageProvider(store common.StorageFactory) ManagerOption {
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

// NewManager creates and initializes a new CertManager, applies all provided options,
func NewManager(log *log.PrefixLogger, opts ...ManagerOption) (*CertManager, error) {
	var err error

	cm := &CertManager{
		log:          log,
		configs:      make(map[string]common.ConfigProvider),
		provisioners: make(map[string]common.ProvisionerFactory),
		storages:     make(map[string]common.StorageFactory),
	}

	for _, opt := range opts {
		if optErr := opt(cm); optErr != nil {
			return nil, fmt.Errorf("failed to apply option: %w", optErr)
		}
	}

	for _, cp := range cm.configs {
		if err := cm.validateConfig(cp); err != nil {
			return nil, err
		}
	}

	// If no certificate storage was set via options, initialize default storage without state.
	if cm.certificates == nil {
		if cm.certificates, err = newCertStorage(nil); err != nil {
			return nil, fmt.Errorf("failed to initialize default cert storage: %w", err)
		}
	}

	if cm.syncInterval == 0 {
		cm.syncInterval = DefaultSyncInterval
	}

	cm.processingQueue = NewCertificateProcessingQueue(cm.processCertificate)
	return cm, nil
}

// Run starts the certificate manager's background processes, including the processing queue
// and periodic synchronization loop. It runs until the provided context is canceled.
func (cm *CertManager) Run(ctx context.Context) {
	go cm.processingQueue.Run(ctx)

	ticker := time.NewTicker(cm.syncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := cm.sync(ctx); err != nil {
				cm.log.Errorf("certificate management sync failed: %v", err)
			}
		}
	}
}

/*
	- store processed certificated from all providers
		- in the end, remove those a are not processed -> storage provider.
	- check if certificate exists
		- Not exists:
			- can we we find it in the storage provider?
				- yes, add it and keep the validate flow.
				- no, add it and add to process queue, and continue next.
		- exists:
			- try lock cert, unable (e.g. processing?) continue
			- still in processing (ask the queue)? continue
			- check certificate saved config, if was changed, update it (should we remove the cert from the storage provider??):
				- are we able to understand if the storage config has changed? if so, remove existing?
				- add it and add to process queue
				- continue
			- check certificate info
				- Ask storage provider for the certificate and fill info struct.
				- if it's not ready (e.g., certificate not exists) -> add it and add to process queue -> continue
				- if it's ready (i.e., got response from storage provider):
					- check expired?




*/

func (cm *CertManager) sync(ctx context.Context) error {
	handledProviders := make([]string, 0, len(cm.configs))

	defer func() {
		defer cm.cleanupUntrackedProviders(handledProviders)
	}()

	for providerName, cfgProvider := range cm.configs {
		handledProviders = append(handledProviders, providerName)

		if err := cm.syncProvider(ctx, cfgProvider); err != nil {
			cm.log.Errorf("syncProvider failed for %q: %v", providerName, err)
		}
	}
	return nil
}

func (cm *CertManager) syncProvider(ctx context.Context, provider common.ConfigProvider) error {
	handledCertificates := make([]string, 0)
	providerName := provider.Name()

	defer func() {
		cm.cleanupUntrackedCertificates(providerName, handledCertificates)
	}()

	configs, err := provider.GetCertificateConfigs()
	if err != nil {
		cm.log.Errorf("failed to load configs from provider %q: %v", providerName, err)

		// Mark existing certificates as handled so they won't be deleted
		providerObj, err := cm.certificates.EnsureProvider(providerName)
		if err != nil {
			return err
		}
		for _, cert := range providerObj.Certificates {
			handledCertificates = append(handledCertificates, cert.Name)
		}
		return fmt.Errorf("failed to load certificate configs from provider %q: %w", providerName, err)
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

func (cm *CertManager) syncCertificate(ctx context.Context, provider common.ConfigProvider, cfg common.CertificateConfig) error {
	providerName := provider.Name()
	certName := cfg.Name

	cert, err := cm.certificates.ReadCertificate(providerName, certName)
	if err != nil {
		// Try loading from storage
		s, initErr := cm.initStorageProvider(cfg)
		if initErr != nil {
			return fmt.Errorf("failed to init storage provider for certificate %q from provider %q: %w", certName, providerName, initErr)
		}

		if _, loadErr := s.LoadCertificate(ctx); loadErr != nil {
			cm.addCertificate(ctx, providerName, certName, cfg)
			cm.log.Infof("Created new certificate %q for provider %q", certName, providerName)
			return nil
		}

		// Existing cert found in storage — register it
		cert = &certificate{
			Name:   certName,
			Config: cfg,
			Info: CertificateInfo{
				RenewalCount: 0,
			},
		}
		cm.certificates.StoreCertificate(providerName, cert)
		cm.log.Infof("Registered existing certificate %q from storage for provider %q", certName, providerName)
	}

	if !cert.mu.TryRLock() {
		cm.log.Debugf("certificate %q is currently locked (processing), skipping", certName)
		return nil
	}
	defer cert.mu.RUnlock()

	if cm.processingQueue.IsProcessing(providerName, cert.Name) {
		cm.log.Debugf("certificate %q is already in processing queue, skipping", certName)
		return nil
	}

	shouldRenew := cm.renewCertificateIfNeeded(ctx, providerName, certName, cfg, func() bool {
		return !cert.Config.Equal(cfg)
	})

	if shouldRenew {
		cm.log.Infof("Renewal triggered for certificate %q of provider %q", certName, providerName)
		return nil
	}

	cm.log.Debugf("Certificate %q for provider %q is up-to-date", certName, providerName)
	return nil
}

func (cm *CertManager) processCertificate(ctx context.Context, providerName string, cert *certificate, attempt int) *time.Duration {
	cert.mu.Lock()
	defer cert.mu.Unlock()

	/*

		time.Sleep(1 * time.Second)
		if attempt != 2 {
			t := 5 * time.Second
			return &t
		}

		// Check context as early as possible
		if ctx.Err() != nil {
			cert.Info.Err = ctx.Err().Error()
			return nil
		}

		if cert.Storage == nil {
			s, err := cm.initStorage(cert.Config)
			if err != nil {
				cert.Info.Err = err.Error()
				return nil
			}
			cert.Storage = s
		}

		if cert.Provisioner == nil {
			p, err := cm.initProvisioner(cert.Config)
			if err != nil {
				cert.Info.Err = err.Error()
				return nil
			}

			if err := p.Provision(ctx); err != nil {
				cert.Info.Err = err.Error()
				return nil
			}
			cert.Provisioner = p
		}

		// Check readiness
		ready, crtBytes, keyBytes, err := cert.Provisioner.Check(ctx)
		if err != nil {
			cert.Info.Err = err.Error()
			return nil
		}

		if !ready {
			return lo.ToPtr[time.Duration](5 * time.Second)
		}

		if ctx.Err() != nil {
			cert.Info.Err = err.Error()
			return nil
		}

		// Parse certificate
		block, _ := pem.Decode(crtBytes)
		if block == nil {
			cert.Info.Err = "failed to parse PEM block"
			return nil
		}

		parsedCert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			cert.Info.Err = fmt.Sprintf("failed to parse certificate: %s", err)
			return nil
		}

		// Write to storage
		if err := cert.Storage.Write(crtBytes, keyBytes); err != nil {
			cert.Info.Err = err.Error()
			return nil
		}

		// Update info
		cert.Info = CertificateInfo{
			NotAfter:     parsedCert.NotAfter,
			LastWritten:  time.Now(),
			Subject:      parsedCert.Subject.CommonName,
			SerialNumber: parsedCert.SerialNumber.String(),
			Ready:        true,
			RenewalCount: cert.Info.RenewalCount + 1,
		}
		cm.certificates.StoreCertificate(cert)
	*/
	return nil // Done — no requeue
}

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

		cm.log.Infof("Removed untracked provider %q and all associated certificates", providerName)
	}

	return nil
}
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

		cm.log.Infof("Removed untracked certificate %q from provider %q", cert.Name, providerName)
	}

	return nil
}

func (cm *CertManager) addCertificate(ctx context.Context, providerName string, certName string, cfg common.CertificateConfig) {
	cm.renewCertificateIfNeeded(ctx, providerName, certName, cfg, nil)
}

func (cm *CertManager) renewCertificateIfNeeded(ctx context.Context, providerName string, certName string, cfg common.CertificateConfig, shouldUpdate func() bool) bool {
	if shouldUpdate != nil && !shouldUpdate() {
		return false
	}

	var renewcount int
	existing, err := cm.certificates.ReadCertificate(providerName, certName)
	if err == nil && existing != nil {
		cm.processingQueue.Remove(providerName, existing.Name)
		renewcount = existing.Info.RenewalCount + 1
	}

	updatedCert := &certificate{
		Name:   certName,
		Config: cfg,
		Info: CertificateInfo{
			RenewalCount: renewcount,
		},
	}
	cm.certificates.StoreCertificate(providerName, updatedCert)
	cm.processingQueue.Process(ctx, providerName, updatedCert)
	return true
}

/*
	cfg, err := cfgProvider.GetCertificateConfigs()
	if err != nil {
		//TODO
		continue
	}
	for _, c := range cfg {
		name := fmt.Sprintf("%d-%s", i, c.Name)

		existing, err := cm.certificates.ReadCertificate(name)
		if err != nil {
			cm.addCertificate(ctx, name, c)
			continue
		}
		func() {
			if !existing.mu.TryRLock() {
				// Currently being processed — skip
				return
			}
			defer existing.mu.RUnlock()

			if cm.processingQueue.isProcessing(existing.Name) {
				// Still processing — skip
				return
			}

			if cm.renewCertificateIfNeeded(ctx, name, c, func() bool { return !existing.Config.Equal(c) }) {
				return
			}
			if cm.renewCertificateIfNeeded(ctx, name, c, func() bool { return time.Until(existing.Info.NotAfter) < time.Duration(c.RenewalThreshold) }) {
				return
			}



			fmt.Printf("Certificate %s is ready\n", name)
		}()

	}
*/

func (cm *CertManager) validateConfig(cp common.ConfigProvider) error {
	cc, err := cp.GetCertificateConfigs()
	if err != nil {
		return fmt.Errorf("failed to get certificate configs from provider: %w", err)
	}

	for _, cert := range cc {
		p, ok := cm.provisioners[cert.Provisioner.Type]
		if !ok {
			return fmt.Errorf("provisioner type %q not registered", cert.Provisioner.Type)
		}

		if err := p.Validate(cm.log, cert); err != nil {
			return fmt.Errorf("provisioner validation failed for cert %q: %w", cert.Name, err)
		}

		s, ok := cm.storages[cert.Storage.Type]
		if !ok {
			return fmt.Errorf("storage type %q not registered", cert.Storage.Type)
		}

		if err := s.Validate(cm.log, cert); err != nil {
			return fmt.Errorf("storage validation failed for cert %q: %w", cert.Name, err)
		}
	}
	return nil
}

func (cm *CertManager) initProvisioner(cfg common.CertificateConfig) (common.ProvisionerProvider, error) {
	p, ok := cm.provisioners[cfg.Provisioner.Type]
	if !ok {
		return nil, fmt.Errorf("")
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, err
	}
	return p.New(cm.log, cfg)
}

func (cm *CertManager) initStorageProvider(cfg common.CertificateConfig) (common.StorageProvider, error) {
	p, ok := cm.storages[cfg.Storage.Type]
	if !ok {
		return nil, fmt.Errorf("")
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, err
	}
	return p.New(cm.log, cfg)
}
