package dependency_sync_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

const (
	POLLING       = "5s"
	RENDERTIMEOUT = "3m"
	EVENTTIMEOUT  = "4m"

	fastPollInterval = 30 * time.Second
)

var (
	auxSvcs            *auxiliary.Services
	originalConfigYAML string
)

func TestDependencySync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Dependency Sync E2E Suite")
}

// patchPollInterval lowers the dependency-sync poll interval for fast E2E testing.
func patchPollInterval(interval time.Duration) (originalConfig string, err error) {
	providers := setup.GetDefaultProviders()
	if providers == nil {
		return "", fmt.Errorf("providers not initialized")
	}

	// 1. Read the current periodic config and save it for restore after the suite.
	content, err := providers.Infra.GetServiceConfig(infra.ServicePeriodic)
	if err != nil {
		return "", fmt.Errorf("failed to read periodic config: %w", err)
	}
	originalConfig = content

	// 2. Parse the config and inject the faster poll interval.
	var cfg config.Config
	if err := yaml.Unmarshal([]byte(content), &cfg); err != nil {
		return "", fmt.Errorf("failed to parse periodic config: %w", err)
	}

	if cfg.DependenciesSync == nil {
		cfg.DependenciesSync = &config.DependenciesSyncConfig{}
	}
	cfg.DependenciesSync.PollInterval = util.Duration(interval)

	// 3. Write the patched config back to the periodic service.
	updated, err := yaml.Marshal(&cfg)
	if err != nil {
		return originalConfig, fmt.Errorf("failed to marshal periodic config: %w", err)
	}

	if err := providers.Infra.SetServiceConfig(infra.ServicePeriodic, "config.yaml", string(updated)); err != nil {
		return originalConfig, fmt.Errorf("failed to write periodic config: %w", err)
	}

	// 4. Restart the periodic service so it picks up the new interval.
	if err := providers.Lifecycle.Restart(infra.ServicePeriodic); err != nil {
		return originalConfig, fmt.Errorf("failed to restart periodic: %w", err)
	}

	if err := providers.Lifecycle.WaitForReady(infra.ServicePeriodic, 2*time.Minute); err != nil {
		return originalConfig, fmt.Errorf("periodic not ready after restart: %w", err)
	}

	return originalConfig, nil
}

// restorePollInterval restores the original periodic config and restarts the deployment.
func restorePollInterval(original string) error {
	if original == "" {
		return nil
	}
	providers := setup.GetDefaultProviders()
	if providers == nil {
		return fmt.Errorf("providers not initialized during restore")
	}
	if err := providers.Infra.SetServiceConfig(infra.ServicePeriodic, "config.yaml", original); err != nil {
		return fmt.Errorf("failed to restore periodic config: %w", err)
	}
	if err := providers.Lifecycle.Restart(infra.ServicePeriodic); err != nil {
		return fmt.Errorf("failed to restart periodic: %w", err)
	}
	if err := providers.Lifecycle.WaitForReady(infra.ServicePeriodic, 2*time.Minute); err != nil {
		return fmt.Errorf("periodic not ready after restore: %w", err)
	}
	return nil
}

var _ = SynchronizedBeforeSuite(func() []byte {
	ctx := context.Background()
	auxSvcs = auxiliary.Get(ctx)

	fileServerSvcs, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceFileServer})
	Expect(err).ToNot(HaveOccurred(), "failed to start file server")
	auxSvcs.FileServer = fileServerSvcs.FileServer

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	originalConfigYAML, err = patchPollInterval(fastPollInterval)
	Expect(err).ToNot(HaveOccurred(), "patchPollInterval must succeed; tests assume a 30s poll interval")
	GinkgoWriter.Printf("Lowered dependency sync poll interval to %v\n", fastPollInterval)
	return []byte(originalConfigYAML)
}, func(data []byte) {
	originalConfigYAML = string(data)
	e2e.SetupWorkerHarnessOrAbort()
})

var _ = SynchronizedAfterSuite(func() {
}, func() {
	err := restorePollInterval(originalConfigYAML)
	if err != nil {
		GinkgoWriter.Printf("WARNING: restorePollInterval failed: %v\n", err)
	}
	_ = auxiliary.StopServices([]auxiliary.Service{auxiliary.ServiceFileServer})
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})
