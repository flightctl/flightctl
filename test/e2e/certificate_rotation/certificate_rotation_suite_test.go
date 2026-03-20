package certificate_rotation_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestCertificateRotation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Certificate Rotation E2E Suite")
}

var auxSvcs *auxiliary.Services

var _ = BeforeSuite(func() {
	auxSvcs = auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// Configure the API server to issue short-lived management certificates
	// so that rotation tests complete quickly and TTL-based assertions work.
	lifecycle := setup.GetDefaultProviders().Lifecycle
	err := lifecycle.SetDeploymentEnv(infra.ServiceAPI, "FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS", certExpirySeconds)
	Expect(err).ToNot(HaveOccurred())

	e2e.SetupWorkerHarnessOrAbort()
})

var _ = AfterSuite(func() {
	if providers := setup.GetDefaultProviders(); providers != nil && providers.Lifecycle != nil {
		err := providers.Lifecycle.RemoveDeploymentEnv(infra.ServiceAPI, "FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS")
		Expect(err).ToNot(HaveOccurred())
	}

	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("BeforeEach Worker %d: Setting up test with VM from pool (cert rotation)\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	err := harness.SetupVMFromPool(workerID)
	Expect(err).ToNot(HaveOccurred())

	// Sync the VM clock with the host after snapshot revert. The snapshot
	// preserves the clock state from creation time, so a stale clock
	// causes the certmanager to misjudge certificate expiry windows
	err = harness.SyncVMClock()
	Expect(err).ToNot(HaveOccurred())

	// Create systemd drop-in to configure accelerated certificate rotation
	dropInContent := `[Service]
Environment="FLIGHTCTL_TEST_CERT_MANAGER_SYNC_INTERVAL=` + certManagerSyncInterval + `"
Environment="FLIGHTCTL_TEST_MGMT_CERT_RENEW_BEFORE_SECONDS=` + certRenewBeforeSeconds + `"
Environment="FLIGHTCTL_TEST_MGMT_CERT_BACKOFF_MAX=` + certBackoffMax + `"
`
	err = harness.CreateAgentDropIn("cert-rotation-test.conf", dropInContent)
	Expect(err).ToNot(HaveOccurred())

	err = harness.EnableAgentMetrics()
	Expect(err).ToNot(HaveOccurred())

	err = harness.VMDaemonReload()
	Expect(err).ToNot(HaveOccurred())

	err = harness.StartFlightCtlAgent()
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("BeforeEach Worker %d: Test setup completed (cert rotation)\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("AfterEach Worker %d: Cleaning up test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("AfterEach Worker %d: Test cleanup completed\n", workerID)
})
