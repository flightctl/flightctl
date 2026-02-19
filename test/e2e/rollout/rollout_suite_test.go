package rollout_test

import (
	"testing"

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
const DEVICEWAITTIME = "30s"
const DEFAULTUPDATETIMEOUT = "90s"

func TestRollout(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rollout Suite")
}

var _ = BeforeSuite(func() {
	// Setup harness without VM for rollout tests
	// Rollout tests only need API access, device VMs are created separately with worker IDs 1000+
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test (no VM needed for main harness)\n", workerID)

	// Create test-specific context for proper tracing
	testCtx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(testCtx)

	// No VM setup needed - rollout tests use device VMs (worker IDs 1000+) only
	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Get the harness and context directly - no shared variables needed
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
