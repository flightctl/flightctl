package helm_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHelm(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Helm E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	// Every spec in this suite is needvm (it switches device.Spec.Os to the MicroShift v12
	// variant), so BeforeEach still gets a VM - but creating it lazily means a shard whose
	// specs are all filtered out or skipped does not pay the boot.
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	auxFuture.Wait()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with %s from pool\n", workerID, e2e.DeviceBackendName())

	// Create test-specific context for proper tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Backend selection goes through the shared dispatcher so it stays label-driven even though
	// this suite is currently needvm throughout; if a MicroShift-capable container device image
	// ever lands, flipping the Describe labels is the only change needed here.
	err := harness.SetupDeviceForCurrentSpec(workerID)
	Expect(err).ToNot(HaveOccurred())
	if err := configurePersistentJournaldForHelmDiagnostics(harness); err != nil {
		GinkgoWriter.Printf("Warning: persistent journal setup failed; continuing without persistent Helm diagnostics: %v\n", err)
	}

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Get the harness and context directly - no shared variables needed
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	captureHelmFailureDiagnostics(harness)
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
