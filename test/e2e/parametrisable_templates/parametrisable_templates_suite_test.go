package parametrisabletemplates

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var auxSvcs *auxiliary.Services

const TIMEOUT = "5m"
const POLLING = "125ms"
const LONGTIMEOUT = "10m"

func TestParametrisableTemplates(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ParametrisableTemplates E2E Suite")
}

var _ = BeforeSuite(func() {
	ctx := context.Background()
	auxFuture := e2e.StartAuxServicesAsync(ctx)

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()

	auxSvcs = auxFuture.Wait()
	fileServerSvcs, err := auxiliary.StartServices(ctx, []auxiliary.Service{auxiliary.ServiceFileServer})
	Expect(err).ToNot(HaveOccurred(), "failed to start file server")
	auxSvcs.FileServer = fileServerSvcs.FileServer
})

var _ = AfterSuite(func() {
	_ = auxiliary.StopServices([]auxiliary.Service{auxiliary.ServiceFileServer})

	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
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
