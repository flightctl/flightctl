package rollout_test

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "5m"
const POLLING = "125ms"
const FASTPOLLING = "100ms" // Fast polling for catching quick batch transitions
const POLLINGINTERVAL = "10s"
const MEDIUMTIMEOUT = "10m"
const LONGTIMEOUT = "15m"
const DEFAULTUPDATETIMEOUT = "90s"

var auxSvcs *auxiliary.Services
var workerHarness *e2e.Harness

func TestRollout(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rollout Suite")
}

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	registryCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	auxiliary.ConfigureDockerHost()
	auxSvcs, err = auxiliary.StartServices(registryCtx, []auxiliary.Service{auxiliary.ServiceRegistry})
	Expect(err).NotTo(HaveOccurred())
})

var _ = AfterSuite(func() {
	// In CI, cleanup containers; in local dev, leave running for speed
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	workerHarness = e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test (no VM needed for main harness)\n", workerID)

	// Create test-specific context for proper tracing
	testCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	workerHarness.SetTestContext(testCtx)

	// No VM setup needed - rollout tests use device VMs (worker IDs 1000+) only
	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	workerHarness.PrintAgentLogsIfFailed()
	workerHarness.CaptureDeploymentLogsIfFailed()

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := workerHarness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	workerHarness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
