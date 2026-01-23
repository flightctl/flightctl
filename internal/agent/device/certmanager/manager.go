package certmanager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider/management"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	pkgcertmanager "github.com/flightctl/flightctl/pkg/certmanager"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	managementBundleName = "device-management"

	certsBundleName       = "certs-config-yaml"
	certsBundleConfigFile = "certs.yaml"

	defaultSyncInterval = time.Hour
	renewBeforeExpiry   = 30 * 24 * time.Hour
)

type AgentCertManager struct {
	cm  *pkgcertmanager.CertManager
	log *log.PrefixLogger
}

// NewAgentCertManager wires the pkg certmanager with agent-specific providers/factories.
func NewAgentCertManager(
	ctx context.Context,
	log *log.PrefixLogger,
	cfg *config.Config,
	deviceName string,
	managementClient client.Management,
	readWriter fileio.ReadWriter,
	idFactory identity.ExportableFactory,
	identityProvider identity.Provider,
	statusManager status.Manager,
	systemInfoManager systeminfo.Manager,
) (*AgentCertManager, error) {
	if log == nil {
		return nil, fmt.Errorf("logger is nil")
	}
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}
	if readWriter == nil {
		return nil, fmt.Errorf("readWriter is nil")
	}
	if deviceName == "" {
		return nil, fmt.Errorf("deviceName is empty")
	}
	if idFactory == nil {
		return nil, fmt.Errorf("idFactory is nil")
	}

	// Base provisioner: issues/renews the management certificate.
	managementProvisionerFactory := management.NewManagementProvisionerFactory(
		deviceName,
		identityProvider,
		managementClient,
	)

	// Observe provisioning attempts, outcomes, and duration for metrics.
	managementProvisionerFactory = management.WithCertMetricsProvisioner(
		cfg.GetManagementCertMetricsCallback(),
		managementProvisionerFactory,
	)

	// Base storage: persists the management certificate.
	managementStorageFactory := management.NewManagementStorageFactory(
		identityProvider,
		managementClient,
	)

	// Observe completed renewal outcomes on successful store.
	managementStorageFactory = management.WithCertMetricsOnStore(
		cfg.GetManagementCertMetricsCallback(),
		managementStorageFactory,
	)

	// Trigger system-info and status collection after cert changes.
	managementStorageFactory = management.WithStatusCollectOnStore(
		ctx,
		log,
		identityProvider,
		systemInfoManager,
		statusManager,
		managementStorageFactory,
	)

	managementBundle, err := pkgcertmanager.NewBundle(
		managementBundleName,
		pkgcertmanager.WithConfigProvider(
			management.NewManagementConfigProvider(renewBeforeExpiry),
		),
		pkgcertmanager.WithProvisionerFactory(managementProvisionerFactory),
		pkgcertmanager.WithStorageFactory(managementStorageFactory),
	)
	if err != nil {
		return nil, fmt.Errorf("new %q bundle: %w", managementBundleName, err)
	}

	certsBundle, err := pkgcertmanager.NewBundle(
		certsBundleName,
		pkgcertmanager.WithConfigProvider(
			provider.NewDropInConfigProvider(readWriter, filepath.Join(cfg.ConfigDir, certsBundleConfigFile)),
		),
		pkgcertmanager.WithProvisionerFactory(
			provider.NewCSRProvisionerFactory(deviceName, managementClient, idFactory),
		),
		pkgcertmanager.WithStorageFactory(
			provider.NewFileSystemStorageFactory(readWriter),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("new %q bundle: %w", certsBundleName, err)
	}

	cm, err := pkgcertmanager.NewManager(ctx, log,
		pkgcertmanager.WithBundleProvider(managementBundle),
		pkgcertmanager.WithBundleProvider(certsBundle),
	)
	if err != nil {
		return nil, fmt.Errorf("new cert manager: %w", err)
	}

	return &AgentCertManager{
		cm:  cm,
		log: log,
	}, nil
}

// Sync delegates to the pkg cert manager.
// The agent decides when to call Sync (e.g., on a timer, on config change, at startup).
func (a *AgentCertManager) Sync(ctx context.Context, _ *config.Config) error {
	return a.cm.Sync(ctx)
}

// Run periodically calls Sync until ctx is canceled.
func (a *AgentCertManager) Run(ctx context.Context) {
	// First sync immediately.
	if err := a.cm.Sync(ctx); err != nil {
		a.log.Errorf("Initial certificate sync failed: %v", err)
	}

	interval := certManagerSyncInterval()
	a.log.Debugf("Certificate manager sync interval: %s", interval)

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.cm.Sync(ctx); err != nil {
				a.log.Errorf("Certificate sync failed: %v", err)
			}
		}
	}
}

func certManagerSyncInterval() time.Duration {
	if v := os.Getenv("FLIGHTCTL_TEST_CERT_MANAGER_SYNC_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultSyncInterval
}
