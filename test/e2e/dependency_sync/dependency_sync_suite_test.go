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
	"github.com/flightctl/flightctl/test/e2e/infra/quadlet"
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

	// periodicTemplatePath is the host path for the periodic service's config template.
	// ExecStartPre renders this template on every service start/restart.
	periodicTemplatePath = "/usr/share/flightctl/flightctl-periodic/config.yaml.template"

	// dependenciesSyncTemplateBlock is a Go template block appended to the periodic
	// config template so that ExecStartPre renders dependenciesSync from service-config.yaml.
	dependenciesSyncTemplateBlock = `{{- if .dependenciesSync}}
dependenciesSync:
  pollInterval: {{if .dependenciesSync.pollInterval}}{{.dependenciesSync.pollInterval}}{{else}}15m{{end}}
{{- end}}
`
)

var (
	auxSvcs                 *auxiliary.Services
	originalConfigYAML      string
	originalTemplateContent string
)

func TestDependencySync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Dependency Sync E2E Suite")
}

var _ = SynchronizedBeforeSuite(func() []byte {
	ctx := context.Background()
	auxSvcs = auxiliary.Get(ctx)

	fileServerSvcs, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceFileServer})
	Expect(err).ToNot(HaveOccurred(), "failed to start file server")
	auxSvcs.FileServer = fileServerSvcs.FileServer

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// On quadlets, the periodic config template does not include a dependenciesSync
	// section, so ExecStartPre (which re-renders the template on every restart)
	// would drop the patched poll interval. Patch the template before restarting.
	providers := setup.GetDefaultProviders()
	if providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		Expect(managePeriodicTemplate(providers, true)).To(Succeed(), "patch periodic template for quadlet")
	}

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
	// Restore the original periodic template on quadlets first, before
	// restorePollInterval which restarts the service. If restore runs after
	// and fails (e.g. restart timeout), the patched template stays on the
	// host permanently — every future periodic restart would render a
	// dependenciesSync section that was never part of the original deployment.
	providers := setup.GetDefaultProviders()
	if providers != nil && providers.Infra.GetEnvironmentType() == infra.EnvironmentQuadlet {
		if err := managePeriodicTemplate(providers, false); err != nil {
			GinkgoWriter.Printf("WARNING: failed to restore periodic template: %v\n", err)
		}
	}

	err := restorePollInterval(originalConfigYAML)
	if err != nil {
		GinkgoWriter.Printf("WARNING: restorePollInterval failed: %v\n", err)
	}

	_ = auxiliary.StopServices([]auxiliary.Service{auxiliary.ServiceFileServer})
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

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

// managePeriodicTemplate patches or restores the periodic config template on the
// quadlet host. When patch is true, it appends a dependenciesSync block to the
// template so ExecStartPre renders it from service-config.yaml. When patch is
// false, it restores the original template and removes the stale dependenciesSync
// key from service-config.yaml.
func managePeriodicTemplate(providers *infra.Providers, patch bool) error {
	quadletInfra, ok := providers.Infra.(*quadlet.InfraProvider)
	if !ok {
		return fmt.Errorf("expected quadlet infra provider")
	}

	if patch {
		content, err := quadletInfra.ReadHostFile(periodicTemplatePath)
		if err != nil {
			return fmt.Errorf("read periodic template: %w", err)
		}
		originalTemplateContent = content

		return quadletInfra.WriteHostFile(periodicTemplatePath, []byte(content+dependenciesSyncTemplateBlock))
	}

	// Restore the original template.
	if originalTemplateContent == "" {
		return nil
	}
	if err := quadletInfra.WriteHostFile(periodicTemplatePath, []byte(originalTemplateContent)); err != nil {
		return fmt.Errorf("restore periodic template: %w", err)
	}

	// Remove stale dependenciesSync from service-config.yaml. The mapping
	// leaves it behind because the original config doesn't contain the key.
	raw, err := quadletInfra.GetStandaloneServiceConfig()
	if err != nil {
		return fmt.Errorf("read service-config.yaml for cleanup: %w", err)
	}
	var svcCfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(raw), &svcCfg); err != nil {
		return fmt.Errorf("parse service-config.yaml for cleanup: %w", err)
	}
	if _, exists := svcCfg["dependenciesSync"]; exists {
		delete(svcCfg, "dependenciesSync")
		out, err := yaml.Marshal(svcCfg)
		if err != nil {
			return fmt.Errorf("marshal service-config.yaml after cleanup: %w", err)
		}
		if err := quadletInfra.SetStandaloneServiceConfig(string(out)); err != nil {
			return fmt.Errorf("write cleaned service-config.yaml: %w", err)
		}
	}
	return nil
}
