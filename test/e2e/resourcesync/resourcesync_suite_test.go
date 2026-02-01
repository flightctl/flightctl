package resourcesync_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/satellite"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var satellites *satellite.Services

func TestResourcesync(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Resourcesync E2E Suite")
}

var _ = BeforeSuite(func() {
	satellites = satellite.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
})

var _ = AfterSuite(func() {
	// In CI, cleanup containers; in local dev, leave running for speed
	if satellites != nil {
		satellites.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test\n", workerID)

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

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
