package catalog_refs_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCatalogRefs(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Catalog Item References E2E Suite")
}

var auxSvcs *auxiliary.Services

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	auxSvcs = auxFuture.Wait()
})

var _ = AfterSuite(func() {
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Backend selection is label-driven: a spec-level NeedVMLabel overrides the suite-level
	// NeedContainerLabel, so the specs that really switch the device's OS image still get a VM.
	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with %s from pool\n", workerID, e2e.DeviceBackendName())
	err := harness.SetupDeviceForCurrentSpec(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
