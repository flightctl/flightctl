package parametrisabletemplates

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var satellites *infra.SatelliteServices

const TIMEOUT = "5m"
const POLLING = "125ms"
const LONGTIMEOUT = "10m"

func TestParametrisableTemplates(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ParametrisableTemplates E2E Suite")
}

var _ = BeforeSuite(func() {
	// Get satellite services (starts if needed, reuses if available)
	satellites = infra.GetInfra(context.Background())
	satellites.SetEnvVars()

	// Setup VM and harness for this worker
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	// In CI, cleanup containers; in local dev, leave running for speed
	if satellites != nil {
		satellites.Cleanup(context.Background())
	}
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
