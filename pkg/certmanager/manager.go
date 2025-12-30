package certmanager

import (
	"context"
	"crypto/x509"
	"fmt"
	"strings"
	"sync"
	"time"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	DefaultRenewBeforeExpiry = 30 * 24 * time.Hour // 30 days
	DefaultRequeueDelay      = 10 * time.Second
	DefaultBundleName        = "default"
)

// bundleRegistry limits which provisioners/storages a set of config providers may reference.
// A certificate config can only use factories registered in the same bundle.
type bundleRegistry struct {
	mu   sync.Mutex
	name string

	// disableRenewal disables time-based renewal for all certs in this bundle.
	// This does not prevent initial provisioning or reconciliation on config changes.
	disableRenewal bool
	// Configuration providers (agent-config, file, static)
	configs map[string]ConfigProvider
	// Certificate provisioner factories (CSR, self-signed, empty)
	provisioners map[string]ProvisionerFactory
	// Storage provider factories (filesystem, empty)
	storages map[string]StorageFactory
}

func newBundleRegistry(name string, disableRenewal bool) *bundleRegistry {
	if strings.TrimSpace(name) == "" {
		name = DefaultBundleName
	}
	return &bundleRegistry{
		name:           name,
		disableRenewal: disableRenewal,
		configs:        make(map[string]ConfigProvider),
		provisioners:   make(map[string]ProvisionerFactory),
		storages:       make(map[string]StorageFactory),
	}
}

// providerKey returns the fully-qualified provider identifier for a config provider within a bundle.
func providerKey(bundleName, configName string) string {
	return "bundle:" + bundleName + "/config:" + configName
}

// providerKeyPrefix returns the prefix that matches all provider keys belonging to the given bundle.
func providerKeyPrefix(bundleName string) string {
	return "bundle:" + bundleName + "/config:"
}

// CertManager manages the complete certificate lifecycle for flight control agents.
type CertManager struct {
	log    Logger
	syncMu sync.Mutex

	// In-memory certificate state
	certificates *certStorage
	// bundles isolate which provisioners/storages each config-provider set may use
	bundles map[string]*bundleRegistry
	// certificateReconciler runs async provisioning attempts
	certificateReconciler *CertificateReconciler
}

// ManagerOption defines a functional option for configuring CertManager during initialization.
type ManagerOption func(*CertManager) error

// WithBundleProvider registers a bundle during CertManager construction.
func WithBundleProvider(bp BundleProvider) ManagerOption {
	return func(cm *CertManager) error {
		if bp == nil {
			return fmt.Errorf("bundle is nil")
		}

		bundleName := strings.TrimSpace(bp.Name())
		if bundleName == "" {
			return fmt.Errorf("bundle name is empty")
		}
		if _, exists := cm.bundles[bundleName]; exists {
			return fmt.Errorf("bundle %q already exists", bundleName)
		}

		b := newBundleRegistry(bundleName, bp.DisableRenewal())

		cfgs := bp.Configs()
		for key, cp := range cfgs {
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("bundle %q: config provider key is empty", bundleName)
			}
			if cp == nil {
				return fmt.Errorf("bundle %q: config provider %q is nil", bundleName, key)
			}

			cpName := strings.TrimSpace(cp.Name())
			if cpName == "" {
				return fmt.Errorf("bundle %q: config provider %q has empty Name()", bundleName, key)
			}
			if key != cpName {
				return fmt.Errorf("bundle %q: config provider key %q must match ConfigProvider.Name() %q", bundleName, key, cpName)
			}
			if _, exists := b.configs[key]; exists {
				return fmt.Errorf("bundle %q: duplicate config provider %q", bundleName, key)
			}

			b.configs[key] = cp
		}

		provs := bp.Provisioners()
		for key, pf := range provs {
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("bundle %q: provisioner key is empty", bundleName)
			}
			if pf == nil {
				return fmt.Errorf("bundle %q: provisioner factory %q is nil", bundleName, key)
			}

			t := strings.TrimSpace(pf.Type())
			if t == "" {
				return fmt.Errorf("bundle %q: provisioner factory %q has empty Type()", bundleName, key)
			}
			if key != t {
				return fmt.Errorf("bundle %q: provisioner key %q must match ProvisionerFactory.Type() %q", bundleName, key, t)
			}
			if _, exists := b.provisioners[key]; exists {
				return fmt.Errorf("bundle %q: duplicate provisioner type %q", bundleName, key)
			}

			b.provisioners[key] = pf
		}

		stores := bp.Storages()
		for key, sf := range stores {
			key = strings.TrimSpace(key)
			if key == "" {
				return fmt.Errorf("bundle %q: storage key is empty", bundleName)
			}
			if sf == nil {
				return fmt.Errorf("bundle %q: storage factory %q is nil", bundleName, key)
			}

			t := strings.TrimSpace(sf.Type())
			if t == "" {
				return fmt.Errorf("bundle %q: storage factory %q has empty Type()", bundleName, key)
			}
			if key != t {
				return fmt.Errorf("bundle %q: storage key %q must match StorageFactory.Type() %q", bundleName, key, t)
			}
			if _, exists := b.storages[key]; exists {
				return fmt.Errorf("bundle %q: duplicate storage type %q", bundleName, key)
			}

			b.storages[key] = sf
		}

		// Bundle sanity checks: a bundle must be able to produce and persist certs.
		if len(b.configs) == 0 {
			return fmt.Errorf("bundle %q: no config providers registered", bundleName)
		}
		if len(b.provisioners) == 0 {
			return fmt.Errorf("bundle %q: no provisioner factories registered", bundleName)
		}
		if len(b.storages) == 0 {
			return fmt.Errorf("bundle %q: no storage factories registered", bundleName)
		}

		cm.bundles[bundleName] = b
		return nil
	}
}

// NewManager creates and initializes a new CertManager with the provided options.
func NewManager(ctx context.Context, log Logger, opts ...ManagerOption) (*CertManager, error) {
	if log == nil {
		return nil, fmt.Errorf("logger is nil")
	}

	cm := &CertManager{
		log:          log,
		bundles:      make(map[string]*bundleRegistry),
		certificates: newCertStorage(),
	}

	for _, opt := range opts {
		if optErr := opt(cm); optErr != nil {
			return nil, fmt.Errorf("failed to apply option: %w", optErr)
		}
	}

	cm.certificateReconciler = newCertificateReconciler(cm.ensureCertificate)
	go cm.certificateReconciler.Run(ctx)
	return cm, nil
}

// Sync performs a full synchronization of all certificate providers.
func (cm *CertManager) Sync(ctx context.Context) error {
	cm.syncMu.Lock()
	defer cm.syncMu.Unlock()

	cm.log.Debug("Starting certificate sync")
	if err := cm.sync(ctx); err != nil {
		cm.log.Errorf("certificate management sync failed: %v", err)
		return err
	}
	return nil
}

// SyncBundle performs a synchronization for a single bundle by name.
func (cm *CertManager) SyncBundle(ctx context.Context, bundleName string) error {
	cm.syncMu.Lock()
	defer cm.syncMu.Unlock()

	bundleName = strings.TrimSpace(bundleName)
	if bundleName == "" {
		return fmt.Errorf("bundle name is empty")
	}

	cm.log.Debugf("Starting certificate sync for bundle %q", bundleName)

	b, ok := cm.bundles[bundleName]
	if !ok {
		return fmt.Errorf("bundle %q not found", bundleName)
	}

	if err := cm.syncBundle(ctx, b); err != nil {
		cm.log.Errorf("certificate management sync failed for bundle %q: %v", bundleName, err)
		return err
	}

	return nil
}

func (cm *CertManager) sync(ctx context.Context) error {
	var errs []error

	for _, b := range cm.bundles {
		if err := cm.syncBundle(ctx, b); err != nil {
			// Keep going; return aggregated errors.
			cm.log.Errorf("syncBundle failed for bundle %q: %v", b.name, err)
			errs = append(errs, fmt.Errorf("bundle %q: %w", b.name, err))
		}
	}

	return utilerrors.NewAggregate(errs)
}

func (cm *CertManager) syncBundle(ctx context.Context, b *bundleRegistry) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var errs []error

	keepProviders := make(map[string]struct{}, len(b.configs))

	for cfgName, cfgProvider := range b.configs {
		pk := providerKey(b.name, cfgName)
		keepProviders[pk] = struct{}{}

		if err := cm.syncConfigProvider(ctx, b, pk, cfgProvider); err != nil {
			// Conservative behavior: if a provider fails to load, we preserve existing cert state.
			cm.log.Errorf("syncConfigProvider failed for %q: %v", pk, err)
			errs = append(errs, fmt.Errorf("provider %q: %w", pk, err))
		}
	}

	// Bundle-scoped cleanup: remove providers belonging to this bundle that are no longer configured.
	if err := cm.cleanupUntrackedProvidersForBundle(b.name, keepProviders); err != nil {
		errs = append(errs, fmt.Errorf("cleanup providers for bundle %q: %w", b.name, err))
	}

	return utilerrors.NewAggregate(errs)
}

func (cm *CertManager) syncConfigProvider(ctx context.Context, b *bundleRegistry, pk string, cfgProvider ConfigProvider) error {
	configs, err := cfgProvider.GetCertificateConfigs()
	if err != nil {
		// Keep existing certs; skip cleanup for this.
		if e := cm.certificates.EnsureProvider(pk); e != nil {
			return fmt.Errorf("ensure provider %q: %w", pk, e)
		}
		cm.log.Errorf("failed to load cert configs for %q: %v (preserving existing certs)", pk, err)
		return fmt.Errorf("load certificate configs for provider %q: %w", pk, err)
	}

	if err := cm.certificates.EnsureProvider(pk); err != nil {
		return err
	}

	var errs []error
	handledCertificates := make([]string, 0, len(configs))

	for _, certConfig := range configs {
		if err := cm.syncCertificate(ctx, b, pk, certConfig); err != nil {
			cm.log.Errorf("syncCertificate failed for %q/%q: %v", pk, certConfig.Name, err)
			errs = append(errs, fmt.Errorf("cert %q: %w", certConfig.Name, err))
		}
		handledCertificates = append(handledCertificates, certConfig.Name)
	}

	if cleanupErr := cm.cleanupUntrackedCertificates(pk, handledCertificates); cleanupErr != nil {
		cm.log.Errorf("cleanupUntrackedCertificates failed for provider %q: %v", pk, cleanupErr)
		errs = append(errs, fmt.Errorf("cleanup certificates for provider %q: %w", pk, cleanupErr))
	}

	return utilerrors.NewAggregate(errs)
}

func (cm *CertManager) syncCertificate(ctx context.Context, b *bundleRegistry, pk string, cfg CertificateConfig) error {
	certName := cfg.Name

	cert, _, err := cm.certificates.GetOrCreateCertificate(pk, certName, func() *certificate {
		return &certificate{Name: certName}
	})
	if err != nil {
		return err
	}

	cert.mu.Lock()
	defer cert.mu.Unlock()

	// Try to load existing cert metadata using the *desired* storage config.
	// If we successfully load a cert, we also set the "last applied" baseline to cfg.
	// If we cannot load a cert, we keep cert.Config empty; provisioning logic will recover.
	if cert.Config.IsEmpty() {
		if st, initErr := cm.initStorageProvider(b, cfg); initErr != nil {
			cm.log.Errorf("failed to init storage provider for certificate %q from providerKey %q: %v", certName, pk, initErr)
		} else if parsed, loadErr := st.LoadCertificate(ctx); loadErr == nil && parsed != nil {
			cm.addCertificateInfo(cert, parsed)

			// We found an existing cert; treat current desired cfg as the baseline for this process.
			cert.Config = cfg
		} else if loadErr != nil {
			cm.log.Debugf("no existing cert loaded for %q/%q: %v", pk, certName, loadErr)
		}
	}

	// If already in processing, only requeue if desired cfg changed vs queued cfg.
	if cm.certificateReconciler.IsProcessing(pk, cert.Name) {
		_, usedCfg := cm.certificateReconciler.Get(pk, cert.Name)
		if !usedCfg.Equal(cfg) {
			cm.certificateReconciler.Remove(pk, cert.Name)
			if err := cm.provisionCertificate(ctx, b, pk, cert, cfg); err != nil {
				return fmt.Errorf("failed to provision certificate %q from providerKey %q: %w", cert.Name, pk, err)
			}
			cm.log.Debugf("Config changed during processing — re-queued provision for certificate %q of providerKey %q", certName, pk)
		}
		return nil
	}

	if !cm.shouldProvisionCertificate(b, pk, cert, cfg) {
		cm.log.Debugf("Certificate %q for providerKey %q: no provision required", certName, pk)
		return nil
	}

	if err := cm.provisionCertificate(ctx, b, pk, cert, cfg); err != nil {
		return fmt.Errorf("failed to provision certificate %q from providerKey %q: %w", cert.Name, pk, err)
	}

	cm.log.Debugf("Provision triggered for certificate %q of providerKey %q", certName, pk)
	return nil
}

// shouldProvisionCertificate determines whether a certificate needs provisioning.
func (cm *CertManager) shouldProvisionCertificate(b *bundleRegistry, pk string, cert *certificate, cfg CertificateConfig) bool {
	// Initial provisioning.
	if cert.Info.NotAfter == nil || cert.Info.NotBefore == nil {
		cm.log.Debugf("Certificate %q for providerKey %q: missing NotBefore/NotAfter — provisioning", cert.Name, pk)
		return true
	}

	// Reconcile config changes regardless of renewal policy.
	if !cert.Config.Provisioner.Equal(cfg.Provisioner) || !cert.Config.Storage.Equal(cfg.Storage) {
		cm.log.Debugf("Certificate %q for providerKey %q: provisioner or storage changed — provisioning", cert.Name, pk)
		return true
	}

	renewBefore := cfg.RenewBeforeExpiry
	if renewBefore <= 0 {
		renewBefore = DefaultRenewBeforeExpiry
	}

	renewAt := cert.Info.NotAfter.Add(-renewBefore)
	if time.Now().Before(renewAt) {
		return false
	}

	// Bundle-wide kill switch for time-based renewal.
	if b != nil && b.disableRenewal {
		cm.log.Debugf(
			"Certificate %q for providerKey %q: renewal is disabled by bundle %q — skipping time-based renewal (renewBefore=%s, notAfter=%s)",
			cert.Name, pk, b.name, renewBefore, cert.Info.NotAfter.Format(time.RFC3339),
		)
		return false
	}

	cm.log.Debugf("Certificate %q for providerKey %q: within renewal window (%s before expiry) — provisioning", cert.Name, pk, renewBefore)
	return true
}

// provisionCertificate queues a certificate for provisioning.
func (cm *CertManager) provisionCertificate(_ context.Context, b *bundleRegistry, pk string, cert *certificate, cfg CertificateConfig) error {
	return cm.certificateReconciler.Enqueue(b.name, pk, cert, cfg)
}

// ensureCertificate is the main certificate processing function called by the processing queue.
func (cm *CertManager) ensureCertificate(ctx context.Context, bundleName, pk string, cert *certificate, cfg *CertificateConfig, attempt int) *time.Duration {
	if cfg == nil {
		cm.log.Errorf("nil configurations for certificate %q from providerKey %q", cert.Name, pk)
		return nil
	}

	b, ok := cm.bundles[bundleName]
	if !ok {
		cm.log.Errorf("unable to find bundle %q for certificate %q from providerKey %q", bundleName, cert.Name, pk)
		return nil
	}

	cert.mu.Lock()
	defer cert.mu.Unlock()

	retryDelay, err := cm.ensureCertificate_do(ctx, b, cert, cfg, attempt)
	if err != nil {
		cm.log.Errorf("failed to ensure certificate %q from providerKey %q: %v", cert.Name, pk, err)

		// On failure, reset provisioner and storage to force re-init next time.
		cert.Provisioner = nil
		cert.Storage = nil
		return nil
	}

	// If no retry delay is returned, consider it final success and reset cached providers.
	if retryDelay == nil {
		cert.Provisioner = nil
		cert.Storage = nil
	}

	return retryDelay
}

// ensureCertificate_do performs the actual certificate provisioning work.
func (cm *CertManager) ensureCertificate_do(ctx context.Context, b *bundleRegistry, cert *certificate, cfg *CertificateConfig, attempt int) (*time.Duration, error) {
	config := *cfg

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if cert.Storage == nil {
		s, err := cm.initStorageProvider(b, config)
		if err != nil {
			return nil, err
		}
		cert.Storage = s
	}

	if cert.Provisioner == nil {
		p, err := cm.initProvisionerProvider(b, config)
		if err != nil {
			return nil, err
		}
		cert.Provisioner = p
	}

	res, err := cert.Provisioner.Provision(ctx, ProvisionRequest{
		Desired:     config,
		LastApplied: cert.Config,
		Attempt:     attempt + 1, // human-friendly attempt count
	})
	if err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if res == nil {
		return nil, fmt.Errorf("provisioner returned nil ProvisionResult")
	}

	if !res.Ready {
		delay := res.RequeueAfter
		if delay <= 0 {
			delay = DefaultRequeueDelay
		}
		return &delay, nil
	}

	// Contract: Ready=true must include a certificate.
	if res.Cert == nil {
		return nil, fmt.Errorf("provisioner returned Ready=true with nil certificate")
	}

	if err := cert.Storage.Store(ctx, StoreRequest{
		Result:      res,
		Desired:     config.Storage,
		LastApplied: cert.Config.Storage,
	}); err != nil {
		return nil, err
	}

	cm.addCertificateInfo(cert, res.Cert)

	// Update last-applied config only on successful write.
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

// cleanupUntrackedProvidersForBundle removes providers belonging to the given bundle that are no longer configured.
func (cm *CertManager) cleanupUntrackedProvidersForBundle(bundleName string, keep map[string]struct{}) error {
	providers, err := cm.certificates.ListProviderKeys()
	if err != nil {
		return fmt.Errorf("list provider keys: %w", err)
	}

	prefix := providerKeyPrefix(bundleName)
	for _, pk := range providers {
		if !strings.HasPrefix(pk, prefix) {
			continue // other bundle
		}
		if _, ok := keep[pk]; ok {
			continue // still configured
		}

		certs, err := cm.certificates.ReadCertificates(pk)
		if err != nil {
			cm.log.Errorf("failed to read certificates for providerKey %q: %v", pk, err)
			continue
		}

		// Best-effort: cancel/remove any queued work for certs under this provider,
		// while holding cert.mu so ensure/provision can't race on this cert object.
		for _, cert := range certs {
			cert.mu.Lock()
			if cm.certificateReconciler.IsProcessing(pk, cert.Name) {
				cm.certificateReconciler.Remove(pk, cert.Name)
			}
			cert.mu.Unlock()
		}

		if err := cm.certificates.RemoveProvider(pk); err != nil {
			cm.log.Errorf("failed to remove providerKey %q: %v", pk, err)
			continue
		}

		cm.log.Debugf("Removed untracked providerKey %q and all associated certificates", pk)
	}

	return nil
}

// cleanupUntrackedCertificates removes certificates that are no longer configured.
func (cm *CertManager) cleanupUntrackedCertificates(pk string, keepCerts []string) error {
	if strings.TrimSpace(pk) == "" {
		return fmt.Errorf("provider key is empty")
	}

	keep := make(map[string]struct{}, len(keepCerts))
	for _, name := range keepCerts {
		keep[name] = struct{}{}
	}

	certs, err := cm.certificates.ReadCertificates(pk)
	if err != nil {
		return fmt.Errorf("read certificates for providerKey %q: %w", pk, err)
	}

	for _, cert := range certs {
		if _, ok := keep[cert.Name]; ok {
			continue
		}

		// Prevent queue ensure/provision from operating on this cert while we dequeue + delete.
		cert.mu.Lock()
		if cm.certificateReconciler.IsProcessing(pk, cert.Name) {
			cm.certificateReconciler.Remove(pk, cert.Name)
		}
		cert.mu.Unlock()

		if err := cm.certificates.RemoveCertificate(pk, cert.Name); err != nil {
			cm.log.Errorf("failed to remove certificate %q from providerKey %q: %v", cert.Name, pk, err)
			continue
		}

		cm.log.Debugf("Removed untracked certificate %q from providerKey %q", cert.Name, pk)
	}

	return nil
}

// initProvisionerProvider creates a provisioner provider from the certificate configuration.
func (cm *CertManager) initProvisionerProvider(b *bundleRegistry, cfg CertificateConfig) (ProvisionerProvider, error) {
	if strings.TrimSpace(string(cfg.Provisioner.Type)) == "" {
		return nil, fmt.Errorf("provisioner type is not set for certificate %q", cfg.Name)
	}

	p, ok := b.provisioners[string(cfg.Provisioner.Type)]
	if !ok {
		return nil, fmt.Errorf("provisioner type %q not registered", cfg.Provisioner.Type)
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, fmt.Errorf("validation failed for provisioner type %q: %w", cfg.Provisioner.Type, err)
	}

	return p.New(cm.log, cfg)
}

// initStorageProvider creates a storage provider from the certificate configuration.
func (cm *CertManager) initStorageProvider(b *bundleRegistry, cfg CertificateConfig) (StorageProvider, error) {
	if strings.TrimSpace(string(cfg.Storage.Type)) == "" {
		return nil, fmt.Errorf("storage type is not set for certificate %q", cfg.Name)
	}

	p, ok := b.storages[string(cfg.Storage.Type)]
	if !ok {
		return nil, fmt.Errorf("storage type %q not registered", cfg.Storage.Type)
	}

	if err := p.Validate(cm.log, cfg); err != nil {
		return nil, fmt.Errorf("validation failed for storage type %q: %w", cfg.Storage.Type, err)
	}

	return p.New(cm.log, cfg)
}
