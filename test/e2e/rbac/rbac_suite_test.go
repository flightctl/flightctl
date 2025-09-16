package rbac_test

import (
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestRbac(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "RBAC E2E Suite")
}

var (
	flightCtlNs string
)

var _ = BeforeSuite(func() {
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())

	// Check if ACM is installed before running any tests
	isAcmInstalled, err := util.IsAcmInstalled()

	if err != nil {
		GinkgoWriter.Printf("Error while checking if ACM is installed: %s", err)
	}
	if !isAcmInstalled {
		Skip("Skipping test suite because ACM is not installed.")
	}
})

var _ = BeforeEach(func() {
	flightCtlNs = os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs == "" {
		Skip("FLIGHTCTL_NS environment variable should be set")
	}

	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
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
