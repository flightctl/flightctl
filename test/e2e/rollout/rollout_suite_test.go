package rollout_test

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/global_setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "1m"
const POLLING = "125ms"
const LONGTIMEOUT = "2m"

var (
	suiteCtx context.Context
	workerID int
	harness  *e2e.Harness
)

func TestRollout(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rollout Suite")
}

var _ = BeforeSuite(func() {
	e2e.RegisterVMPoolCleanup()
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Rollout Suite")
	workerID = GinkgoParallelProcess()

	GinkgoWriter.Printf("🔄 [BeforeSuite] Worker %d: Starting VM and harness setup\n", workerID)

	// Setup VM for this worker using the global pool
	var err error
	_, err = e2e.SetupVMForWorker(workerID, os.TempDir(), 2233)
	Expect(err).ToNot(HaveOccurred())

	// Create harness once for the entire suite
	harness, err = e2e.NewTestHarnessWithVMPool(suiteCtx, workerID)
	Expect(err).ToNot(HaveOccurred())

	// Run global setup (this will only run once across all test suites)
	global_setup.RunGlobalSetup(harness)

	GinkgoWriter.Printf("✅ [BeforeSuite] Worker %d: VM and harness setup completed successfully\n", workerID)
})

var _ = AfterSuite(func() {
	GinkgoWriter.Printf("🔄 [AfterSuite] Worker %d: Starting cleanup\n", workerID)

	// Clean up harness
	if harness != nil {
		harness.Cleanup(true)
	}

	// Clean up this worker's VM
	err := e2e.CleanupVMForWorker(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [AfterSuite] Worker %d: Cleanup completed successfully\n", workerID)

	// Run global teardown (this will only run once across all test suites)
	global_setup.RunGlobalTeardown()
})

var _ = BeforeEach(func() {
	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Setup VM from pool, revert to pristine snapshot, and start agent
	err := harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := harness.CleanUpTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
