package cli_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// Eventually polling timeout/interval constants
	TIMEOUT      = time.Minute
	LONG_TIMEOUT = 10 * time.Minute
	POLLING      = time.Second
	LONG_POLLING = 10 * time.Second
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
	// Unlike the VM path, starting a container device pulls its image from the aux registry
	// right away, so aux must be ready first - wait on it before setup instead of overlapping
	// (see StartAuxServicesAsync's doc comment, which only holds for the VM path).
	auxSvcs = auxFuture.Wait()
	// This suite only exercises the flightctl CLI against the device (config/status
	// inspection) - it never switches the device's OS image or reboots it, so it doesn't need
	// a real VM (see the container-backed-device-migration plan). Use a container-backed
	// device instead.
	e2e.SetupWorkerHarnessWithContainerDeviceOrAbort()
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := ensureFlightctlLogin(harness)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with container device from pool\n", workerID)

	// Create test-specific context for proper tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Get a pristine container device from the pool and start the agent
	err = harness.SetupContainerFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

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
