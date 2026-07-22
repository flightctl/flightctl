package parametrisabletemplates

import (
	"context"
	"slices"
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

	// Most specs here only exercise fleet parameter template rendering on the device - they never
	// switch the device's OS image or reboot it, so a container-backed device is enough for them
	// (see the container-backed-device-migration plan). The "needvm" spec below is the exception
	// (it puts an OS image on the fleet spec and waits for the rollout to actually apply), so the
	// device itself is set up per-spec in BeforeEach rather than once here.
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	// Unlike the VM path, starting a container device pulls its image from the aux registry
	// right away, so aux must be ready first - wait on it before setup instead of overlapping
	// (see StartAuxServicesAsync's doc comment, which only holds for the VM path).
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

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// "Template variables ... replaced in the device os image" puts an OS image on the fleet spec
	// and waits for the rollout to actually apply it - a real bootc switch + reboot, which a
	// container-backed device can't do ("Detected container; this command requires a booted host
	// system"). Every other spec here only renders fleet parameter templates (configs, labels),
	// never an OS switch, so a container-backed device is sufficient for them.
	var err error
	if slices.Contains(CurrentSpecReport().Labels(), "needvm") {
		GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)
		err = harness.SetupVMFromPoolAndStartAgent(workerID)
	} else {
		GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with container device from pool\n", workerID)
		err = harness.SetupContainerFromPoolAndStartAgent(workerID)
	}
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
