package label_sync_mapping_test

import (
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLabelSyncMapping(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "LabelSyncMapping E2E Suite")
}

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("Setting up LabelSyncMapping resource test with VM for worker %d\n", workerID)
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	Expect(harness.SetupVMFromPoolAndStartAgent(workerID)).To(Succeed())
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
	Expect(harness.CleanUpAllTestResources()).To(Succeed())
	harness.SetTestContext(suiteCtx)
})
