package certmanager

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/certmanager/provider"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/identity"
	pkgcertmanager "github.com/flightctl/flightctl/pkg/certmanager"
)

const (
	defaultBundleName = "certs-config-yaml"
	defaultCertsFile  = "certs.yaml"
)

type AgentCertManager struct {
	cm  *pkgcertmanager.CertManager
	log pkgcertmanager.Logger
}

// NewAgentCertManager wires the pkg certmanager with agent-specific providers/factories.
func NewAgentCertManager(
	ctx context.Context,
	log pkgcertmanager.Logger,
	cfg *config.Config,
	deviceName string,
	managementClient client.Management,
	readWriter fileio.ReadWriter,
	idFactory identity.ExportableFactory,
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

	b, err := pkgcertmanager.NewBundle(
		defaultBundleName,
		pkgcertmanager.WithConfigProvider(
			provider.NewDropInConfigProvider(readWriter, filepath.Join(cfg.ConfigDir, defaultCertsFile)),
		),
		pkgcertmanager.WithProvisionerFactory(
			provider.NewCSRProvisionerFactory(deviceName, managementClient, idFactory),
		),
		pkgcertmanager.WithStorageFactory(
			provider.NewFileSystemStorageFactory(readWriter),
		),
		// Disable time-based renewal to preserve existing agent behavior.
		// Certificate rotation will be enabled explicitly in a follow-up change.
		pkgcertmanager.WithRenewalDisabled(),
	)
	if err != nil {
		return nil, fmt.Errorf("new bundle: %w", err)
	}

	cm, err := pkgcertmanager.NewManager(ctx, log,
		pkgcertmanager.WithBundleProvider(b),
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
	if a == nil || a.cm == nil {
		return fmt.Errorf("cert manager is not initialized")
	}
	return a.cm.Sync(ctx)
}

// Run periodically calls Sync until ctx is canceled.
func (a *AgentCertManager) Run(ctx context.Context, interval time.Duration) error {
	if a == nil || a.cm == nil {
		return fmt.Errorf("cert manager is not initialized")
	}
	if interval <= 0 {
		return fmt.Errorf("interval must be > 0")
	}

	// First sync immediately.
	if err := a.cm.Sync(ctx); err != nil {
		a.log.Errorf("initial certificate sync failed: %v", err)
	}

	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := a.cm.Sync(ctx); err != nil {
				a.log.Errorf("certificate sync failed: %v", err)
			}
		}
	}
}
