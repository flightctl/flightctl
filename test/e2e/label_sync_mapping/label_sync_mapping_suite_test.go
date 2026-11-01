package label_sync_mapping

import (
	"context"
	"testing"
	"time"

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
	SetDefaultEventuallyTimeout(3 * time.Minute)
	SetDefaultEventuallyPollingInterval(time.Second)
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).NotTo(HaveOccurred())
	auxFuture.Wait()
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	harness.SetTestContext(testutil.StartSpecTracerForGinkgo(suiteCtx))
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	harness.CaptureDeploymentLogsIfFailed()
	Expect(harness.CleanUpAllTestResources()).To(Succeed())
	harness.SetTestContext(e2e.GetWorkerContext())
})
