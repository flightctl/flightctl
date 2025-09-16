package selectors

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFieldSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field Selectors E2E Suite")
}

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

var _ = BeforeSuite(func() {
	// Setup VM and harness for this worker
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	// Get base worker ID from environment variable
	workerNum := os.Getenv("GINKGO_WORKER_NUM")
	if workerNum == "" {
		Fail("GINKGO_WORKER_NUM environment variable is required but not set")
	}
	// Create composite worker ID: baseWorkerID + "_proc" + GinkgoParallelProcess()
	workerID := fmt.Sprintf("worker%s_proc%d", workerNum, GinkgoParallelProcess())
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	// Create test-specific context for proper tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Setup VM from pool, revert to pristine snapshot, and start agent
	err := harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	// Get base worker ID from environment variable
	workerNum := os.Getenv("GINKGO_WORKER_NUM")
	if workerNum == "" {
		Fail("GINKGO_WORKER_NUM environment variable is required but not set")
	}
	// Create composite worker ID: baseWorkerID + "_proc" + GinkgoParallelProcess()
	workerID := fmt.Sprintf("worker%s_proc%d", workerNum, GinkgoParallelProcess())
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
