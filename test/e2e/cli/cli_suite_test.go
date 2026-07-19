package cli_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

const (
	// Eventually polling timeout/interval constants
	TIMEOUT      = time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 10 * time.Second
	needVMLabel  = "needvm"

	preparedAgentConfigPath = "bin/agent/etc/flightctl/config.yaml"
	preparedAgentCertsPath  = "bin/agent/etc/flightctl/certs"
	vmAgentConfigPath       = "/etc/flightctl/config.yaml"
	vmAgentCertsPath        = "/etc/flightctl/certs"
	apiHostPrefix           = "api.flightctl."
	agentAPIHostPrefix      = "agent-api.flightctl."
	agentRemoteHostPrefix   = "agent-remote-access.flightctl."
	consoleHostPrefix       = "console-openshift-console."
)

// Initialize suite-specific settings
func init() {
	SetDefaultEventuallyTimeout(TIMEOUT)
	SetDefaultEventuallyPollingInterval(POLLING)
}

var (
	auxSvcs         *auxiliary.Services
	suiteAuthMethod login.AuthMethod
	authMethodKnown bool
)

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	auxSvcs = auxFuture.Wait()
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test\n", workerID)

	// Create test-specific context for proper tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	needsVM := slices.Contains(CurrentSpecReport().Labels(), needVMLabel)
	if !needsVM {
		harness.VM = nil
	}

	_, err := ensureFlightctlLogin(harness)
	Expect(err).ToNot(HaveOccurred())

	if needsVM {
		GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up VM from pool\n", workerID)
		err = setupVMFromPoolAndStartAgent(workerID, harness)
		Expect(err).ToNot(HaveOccurred())
	}

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Get the harness and context directly - no shared variables needed
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})

// TestCLI is the single entry-point that runs the whole spec set.
func TestCLI(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CLI E2E Suite")
}

// setupVMFromPoolAndStartAgent restores a pooled VM, refreshes the agent config
// prepared by the e2e wrapper, and starts the agent.
func setupVMFromPoolAndStartAgent(workerID int, harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	if err := harness.SetupVMFromPool(workerID); err != nil {
		return fmt.Errorf("setting up VM from pool: %w", err)
	}

	if err := refreshPreparedAgentFiles(harness); err != nil {
		return err
	}

	if err := harness.StartAgentWithRetry(); err != nil {
		return err
	}

	return nil
}

// refreshPreparedAgentFiles writes the current run's prepared agent config and
// cert files to the VM after snapshot restore so lazy VM specs do not use stale
// endpoints or enrollment credentials.
func refreshPreparedAgentFiles(harness *e2e.Harness) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if harness.VM == nil {
		return fmt.Errorf("harness VM is nil")
	}

	if err := copyPreparedAgentCerts(harness); err != nil {
		return err
	}

	GinkgoWriter.Printf("🔄 Refreshing agent config from %s\n", preparedAgentConfigPath)
	configBytes, err := os.ReadFile(preparedAgentConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			GinkgoWriter.Printf("Prepared agent config %s does not exist; refreshing restored VM config from API endpoint\n", preparedAgentConfigPath)
			return refreshRestoredAgentConfig(harness)
		}
		return fmt.Errorf("reading prepared agent config %s: %w", preparedAgentConfigPath, err)
	}
	if len(configBytes) == 0 {
		return fmt.Errorf("prepared agent config %s is empty", preparedAgentConfigPath)
	}

	cfg := &agentcfg.Config{}
	if err := yaml.Unmarshal(configBytes, cfg); err != nil {
		return fmt.Errorf("parsing prepared agent config %s: %w", preparedAgentConfigPath, err)
	}

	if err := harness.WriteAgentFile(vmAgentConfigPath, string(configBytes)); err != nil {
		return fmt.Errorf("writing prepared agent config to VM: %w", err)
	}

	return nil
}

// refreshRestoredAgentConfig updates endpoint hostnames in the VM's restored
// config when this run did not prepare injectable agent files.
func refreshRestoredAgentConfig(harness *e2e.Harness) error {
	cfg, err := harness.GetAgentConfig()
	if err != nil {
		return fmt.Errorf("getting restored agent config: %w", err)
	}

	apiEndpoint, err := currentAPIEndpoint()
	if err != nil {
		return err
	}
	apiURL, err := url.Parse(apiEndpoint)
	if err != nil {
		return fmt.Errorf("parsing API endpoint %q: %w", apiEndpoint, err)
	}

	enrollmentHost := agentHostFromAPIHost(apiURL.Hostname(), agentAPIHostPrefix)
	remoteAccessHost := agentHostFromAPIHost(apiURL.Hostname(), agentRemoteHostPrefix)
	consoleHost := consoleHostFromAPIHost(apiURL.Hostname())

	cfg.EnrollmentService.Service.Server = replaceURLHost(cfg.EnrollmentService.Service.Server, enrollmentHost)
	cfg.EnrollmentService.Service.TLSServerName = replaceServerName(cfg.EnrollmentService.Service.TLSServerName, enrollmentHost)
	cfg.ManagementService.Service.Server = replaceURLHost(cfg.ManagementService.Service.Server, enrollmentHost)
	cfg.ManagementService.Service.TLSServerName = replaceServerName(cfg.ManagementService.Service.TLSServerName, enrollmentHost)
	cfg.RemoteAccessService.Service.Server = replaceURLHost(cfg.RemoteAccessService.Service.Server, remoteAccessHost)
	cfg.RemoteAccessService.Service.TLSServerName = replaceServerName(cfg.RemoteAccessService.Service.TLSServerName, remoteAccessHost)
	cfg.EnrollmentService.EnrollmentUIEndpoint = replaceURLHost(cfg.EnrollmentService.EnrollmentUIEndpoint, consoleHost)

	if err := harness.SetAgentConfig(cfg); err != nil {
		return fmt.Errorf("setting refreshed restored agent config: %w", err)
	}

	return nil
}

// currentAPIEndpoint returns the current FlightCtl API endpoint from the e2e wrapper.
func currentAPIEndpoint() (string, error) {
	apiEndpoint := strings.TrimSpace(os.Getenv("API_ENDPOINT"))
	if apiEndpoint == "" {
		return "", fmt.Errorf("API_ENDPOINT environment variable must be set")
	}
	return apiEndpoint, nil
}

// agentHostFromAPIHost derives an agent route host from the public API route host.
func agentHostFromAPIHost(apiHost, agentPrefix string) string {
	if strings.HasPrefix(apiHost, apiHostPrefix) {
		return agentPrefix + strings.TrimPrefix(apiHost, apiHostPrefix)
	}
	GinkgoWriter.Printf("Warning: API host %q does not start with expected prefix %q; using fallback host\n", apiHost, apiHostPrefix)
	return apiHost
}

// consoleHostFromAPIHost derives the OpenShift console host from the public API route host.
func consoleHostFromAPIHost(apiHost string) string {
	if strings.HasPrefix(apiHost, apiHostPrefix) {
		return consoleHostPrefix + strings.TrimPrefix(apiHost, apiHostPrefix)
	}
	GinkgoWriter.Printf("Warning: API host %q does not start with expected prefix %q; using fallback console host\n", apiHost, apiHostPrefix)
	return apiHost
}

// replaceURLHost preserves a URL's scheme, port, and path while replacing the hostname.
func replaceURLHost(rawURL, host string) string {
	if rawURL == "" || host == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return rawURL
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	return parsed.String()
}

// replaceServerName replaces a stale TLS server name while preserving empty names.
func replaceServerName(serverName, host string) string {
	if serverName == "" {
		return serverName
	}
	return host
}

// ensureFlightctlLogin reuses the current client config when it can make an
// authenticated API call, avoiding per-spec flightctl login rate limits.
func ensureFlightctlLogin(harness *e2e.Harness) (login.AuthMethod, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}

	if err := harness.RefreshClient(); err == nil {
		resp, listErr := harness.Client.ListDevicesWithResponse(harness.Context, nil)
		if listErr == nil && resp != nil && resp.StatusCode() == http.StatusOK {
			GinkgoWriter.Printf("Reusing existing flightctl login\n")
			return ensureAuthMethod(harness)
		}
	}

	method, err := login.LoginToAPIWithToken(harness)
	if err != nil {
		return 0, err
	}
	suiteAuthMethod = method
	authMethodKnown = true
	return method, nil
}

// ensureAuthMethod returns the cached admin auth method, resolving it without a
// flightctl login when the suite reuses an already-valid client config.
func ensureAuthMethod(harness *e2e.Harness) (login.AuthMethod, error) {
	if authMethodKnown {
		return suiteAuthMethod, nil
	}

	_, method, err := login.LoginToEnvAsAdmin(harness)
	if err != nil {
		return 0, fmt.Errorf("resolving admin auth method: %w", err)
	}
	suiteAuthMethod = method
	authMethodKnown = true
	return method, nil
}

// copyPreparedAgentCerts mirrors the cert copy done by inject_agent_files_into_qcow.sh
// for VMs that are attached lazily after suite setup.
func copyPreparedAgentCerts(harness *e2e.Harness) error {
	entries, err := os.ReadDir(preparedAgentCertsPath)
	if err != nil {
		if os.IsNotExist(err) {
			GinkgoWriter.Printf("Prepared agent cert dir %s does not exist; skipping cert refresh\n", preparedAgentCertsPath)
			return nil
		}
		return fmt.Errorf("reading prepared agent cert dir %s: %w", preparedAgentCertsPath, err)
	}
	if len(entries) == 0 {
		GinkgoWriter.Printf("Prepared agent cert dir %s is empty; skipping cert refresh\n", preparedAgentCertsPath)
		return nil
	}

	if _, err := harness.VM.RunSSH([]string{"sudo", "mkdir", "-p", vmAgentCertsPath}, nil); err != nil {
		return fmt.Errorf("creating VM agent cert dir %s: %w", vmAgentCertsPath, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		certPath := filepath.Join(preparedAgentCertsPath, entry.Name())
		certBytes, err := os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("reading prepared agent cert %s: %w", certPath, err)
		}
		if len(certBytes) == 0 {
			return fmt.Errorf("prepared agent cert %s is empty", certPath)
		}

		vmCertPath := path.Join(vmAgentCertsPath, entry.Name())
		if err := harness.WriteAgentFile(vmCertPath, string(certBytes)); err != nil {
			return fmt.Errorf("writing prepared agent cert %s to VM: %w", vmCertPath, err)
		}
		if strings.HasSuffix(entry.Name(), ".key") {
			if _, err := harness.VM.RunSSH([]string{"sudo", "chmod", "600", vmCertPath}, nil); err != nil {
				return fmt.Errorf("setting permissions on VM agent key %s: %w", vmCertPath, err)
			}
		}
	}

	return nil
}
