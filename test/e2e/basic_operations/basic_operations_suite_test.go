package basic_operations

import (
	"github.com/flightctl/flightctl/test/e2e/global_setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	workerID int
)

var _ = BeforeSuite(func() {
	e2e.RegisterVMPoolCleanup()
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Basic Operations E2E Suite")
	workerID = GinkgoParallelProcess()

	GinkgoWriter.Printf("🔄 [BeforeSuite] Worker %d: Starting harness setup (no VM needed)\n", workerID)

	// Create harness without VM since this test doesn't need it
	var err error
	harness, err = e2e.NewTestHarnessWithVMPool(suiteCtx, workerID)
	Expect(err).ToNot(HaveOccurred())

	// Remove the VM since this test doesn't need it
	harness.VM = nil

	// Run global setup (this will only run once across all test suites)
	global_setup.RunGlobalSetup(harness)

	GinkgoWriter.Printf("✅ [BeforeSuite] Worker %d: Harness setup completed successfully\n", workerID)
})

var _ = AfterSuite(func() {
	GinkgoWriter.Printf("🔄 [AfterSuite] Worker %d: Starting cleanup\n", workerID)

	// Clean up harness
	if harness != nil {
		harness.Cleanup(false)
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
	}

	GinkgoWriter.Printf("✅ [AfterSuite] Worker %d: Cleanup completed successfully\n", workerID)

	// Run global teardown (this will only run once across all test suites)
	global_setup.RunGlobalTeardown()
})

var _ = BeforeEach(func() {
	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test context\n", workerID)

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

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
