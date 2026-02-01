// Package setup provides provider factory and default-providers for e2e tests.
// It is the single place that constructs K8s or Quadlet providers; the harness
// only consumes *infra.Providers via EnsureDefaultProviders/GetDefaultProviders.
package setup

import (
	"fmt"
	"sync"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/k8s"
	"github.com/flightctl/flightctl/test/e2e/infra/quadlet"
)

var (
	defaultProviders   *infra.Providers
	defaultProvidersMu sync.RWMutex
)

// NewProvidersForEnvironment creates a full set of infra providers for the given config.
// Deployment type is selected once here; all four provider slots are always populated.
func NewProvidersForEnvironment(config *infra.EnvironmentConfig) (*infra.Providers, error) {
	if config == nil {
		config = infra.GetEnvironmentConfig()
	}
	env := config.Type
	if env == "" {
		env = infra.DetectEnvironment()
	}
	switch env {
	case infra.EnvironmentKind, infra.EnvironmentOCP:
		return newK8sProviders(config)
	case infra.EnvironmentQuadlet:
		return newQuadletProviders(config)
	default:
		return nil, fmt.Errorf("unknown environment type %q", env)
	}
}

func newK8sProviders(config *infra.EnvironmentConfig) (*infra.Providers, error) {
	cluster, err := k8s.NewClient()
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}
	namespace := config.GetNamespace()
	infraP, err := k8s.NewInfraProviderWithConfig(namespace, config, cluster)
	if err != nil {
		return nil, fmt.Errorf("k8s infra provider: %w", err)
	}
	lifecycleP := k8s.NewServiceLifecycleProviderWithConfig(cluster, infraP)
	rbacP, err := k8s.NewRBACProvider(cluster)
	if err != nil {
		return nil, fmt.Errorf("k8s rbac: %w", err)
	}
	secretsP, err := k8s.NewSecretsProvider(cluster, infraP)
	if err != nil {
		return nil, fmt.Errorf("k8s secrets: %w", err)
	}
	return &infra.Providers{
		Infra:     infraP,
		Lifecycle: lifecycleP,
		RBAC:      rbacP,
		Secrets:   secretsP,
	}, nil
}

func newQuadletProviders(config *infra.EnvironmentConfig) (*infra.Providers, error) {
	configDir := config.GetConfigDir()
	secretDir := "/etc/flightctl/secrets" //nolint:gosec // G101: path to secret files dir, not a credential
	useSudo := config.UseSudo
	infraP := quadlet.NewInfraProvider(configDir, secretDir, useSudo)
	lifecycleP := quadlet.NewServiceLifecycleProvider(infraP, useSudo)
	rbacP := quadlet.NewPAMRBACProvider(useSudo)
	secretsP := quadlet.NewSecretsProvider(infraP)
	return &infra.Providers{
		Infra:     infraP,
		Lifecycle: lifecycleP,
		RBAC:      rbacP,
		Secrets:   secretsP,
	}, nil
}

// setDefaultProviders stores the providers as the default for this process.
func setDefaultProviders(p *infra.Providers) {
	defaultProvidersMu.Lock()
	defer defaultProvidersMu.Unlock()
	defaultProviders = p
}

// GetDefaultProviders returns the providers set at creation, or nil if not yet set.
func GetDefaultProviders() *infra.Providers {
	defaultProvidersMu.RLock()
	defer defaultProvidersMu.RUnlock()
	return defaultProviders
}

// EnsureDefaultProviders creates and sets default providers if not already set.
// Registry is not from infra; get it from satellite.Get(ctx).RegistryHost/RegistryPort and pass explicitly where needed.
// Call once at harness/suite setup; after that use GetDefaultProviders().
func EnsureDefaultProviders(config *infra.EnvironmentConfig) error {
	if GetDefaultProviders() != nil {
		return nil
	}
	if config == nil {
		config = infra.GetEnvironmentConfig()
	}
	p, err := NewProvidersForEnvironment(config)
	if err != nil {
		return err
	}
	setDefaultProviders(p)
	return nil
}
