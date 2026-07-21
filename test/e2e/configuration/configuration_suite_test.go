package configuration_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "5m"
const POLLING = "125ms"
const LONGTIMEOUT = "10m"

func TestConfiguration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Configuration E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	// This suite only writes/reads config files on the device - it never switches the device's
	// OS image or reboots it, so it doesn't need a real VM (see the
	// container-backed-device-migration plan). Use a container-backed device instead.
	e2e.SetupWorkerHarnessWithContainerDeviceOrAbort()
	auxFuture.Wait()
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Get a pristine container device from the pool and start the agent
	err := harness.SetupContainerFromPoolAndStartAgent(workerID)
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
