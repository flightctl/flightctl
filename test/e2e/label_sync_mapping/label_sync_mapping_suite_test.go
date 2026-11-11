package label_sync_mapping

import (
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLabelSyncMapping(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Label Sync Mapping E2E Suite")
}

func init() {
	SetDefaultEventuallyTimeout(testutil.DURATION_TIMEOUT)
	SetDefaultEventuallyPollingInterval(testutil.POLLING)
}

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).NotTo(HaveOccurred())
})

var _ = BeforeEach(func() {
	workerHarness = e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	workerHarness.SetTestContext(testutil.StartSpecTracerForGinkgo(suiteCtx))
})

var _ = AfterEach(func() {
	workerHarness.CaptureDeploymentLogsIfFailed()
	Expect(workerHarness.CleanUpAllTestResources()).To(Succeed())
	workerHarness.SetTestContext(e2e.GetWorkerContext())
})
