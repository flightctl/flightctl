package parametrisabletemplates

import (
	"context"
	"testing"

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

func TestParametrisableTemplates(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ParametrisableTemplates E2E Suite")
}

var _ = BeforeSuite(func() {
	// Setup VM and harness for this worker
	var err error
	harness, suiteCtx, err = e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())

	workerID = GinkgoParallelProcess()
})

var _ = BeforeEach(func() {
	// Get the harness and context that were set up in BeforeSuite
	workerID = GinkgoParallelProcess()
	harness = e2e.GetWorkerHarness()
	suiteCtx = e2e.GetWorkerContext()

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
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
