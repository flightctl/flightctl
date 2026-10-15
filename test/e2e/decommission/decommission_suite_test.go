package decommission_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	TIMEOUT        = "5m"
	REBOOT_TIMEOUT = "3m"
	POLLING        = "5s"
)

// Agent filesystem paths verified during decommission tests
const (
	agentDataDir     = "/var/lib/flightctl"
	AgentCertPath    = agentDataDir + "/certs/agent.crt"
	DesiredSpecPath  = agentDataDir + "/desired.json"
	CurrentSpecPath  = agentDataDir + "/current.json"
	RollbackSpecPath = agentDataDir + "/rollback.json"
)

func TestCLIDecommission(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Decommission E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	auxFuture.Wait()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Backend selection is label-driven: a spec-level NeedVMLabel overrides the suite-level
	// NeedContainerLabel, so the wipe-and-reboot decommission spec still gets a VM.
	GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up test with %s from pool\n", workerID, e2e.DeviceBackendName())
	err := harness.SetupDeviceForCurrentSpec(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
