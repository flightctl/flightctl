package certificate_rotation_test

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	// apiLoginReadyTimeout is how long AfterSuite waits for flightctl login after
	// restoring the API deployment (route may lag behind pod Ready on OCP).
	apiLoginReadyTimeout = 5 * time.Minute
	apiLoginReadyPolling = 5 * time.Second
)

func TestCertificateRotation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Certificate Rotation E2E Suite")
}

var auxSvcs *auxiliary.Services

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// Configure the API server to issue short-lived management certificates
	// so that rotation tests complete quickly and TTL-based assertions work.
	lifecycle := setup.GetDefaultProviders().Lifecycle
	err := lifecycle.SetDeploymentEnv(infra.ServiceAPI, "FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS", certExpirySeconds)
	Expect(err).ToNot(HaveOccurred())

	// No VM is created here: the device backend is chosen per spec in BeforeEach, so the
	// libvirt pool is only built on demand for the specs that actually need a VM.
	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	auxSvcs = auxFuture.Wait()
})

var _ = AfterSuite(func() {
	if providers := setup.GetDefaultProviders(); providers != nil && providers.Lifecycle != nil {
		err := providers.Lifecycle.RemoveDeploymentEnv(infra.ServiceAPI, "FLIGHTCTL_TEST_MGMT_CERT_EXPIRY_SECONDS")
		Expect(err).ToNot(HaveOccurred())
	}

	// Restore default cert TTL triggers an API rollout. Retry login until the route
	// is ready so the next suite (e.g. cli) does not fail in BeforeEach.
	harness := e2e.GetWorkerHarness()
	Eventually(func() error {
		_, err := login.LoginToAPIWithToken(harness)
		return err
	}).WithTimeout(apiLoginReadyTimeout).WithPolling(apiLoginReadyPolling).Should(Succeed())

	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("BeforeEach Worker %d: Setting up test with %s from pool (cert rotation)\n", workerID, e2e.DeviceBackendName())

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Backend selection is label-driven: a spec-level NeedVMLabel overrides the suite-level
	// NeedContainerLabel, which is exactly what e2e.CurrentSpecUsesContainerDevice encodes.
	var err error
	if e2e.CurrentSpecUsesContainerDevice() {
		// Unlike the VM path, container devices gate flightctl-agent startup behind a marker
		// file that only the harness' container setup creates, so the agent has to be started
		// through the dispatcher here. The accelerated-rotation drop-in and the metrics config
		// written below are picked up by the StartFlightCtlAgent restart at the end of this
		// BeforeEach, before the spec enrolls the device and any management cert is issued.
		// No clock sync is needed either: a fresh container uses the host clock directly
		// instead of resuming a snapshot's stale one.
		err = harness.SetupDeviceForCurrentSpec(workerID)
		Expect(err).ToNot(HaveOccurred())
	} else {
		err = harness.SetupVMFromPool(workerID)
		Expect(err).ToNot(HaveOccurred())

		// Sync the VM clock with the host after snapshot revert. The snapshot
		// preserves the clock state from creation time, so a stale clock
		// causes the certmanager to misjudge certificate expiry windows
		err = harness.SyncVMClock()
		Expect(err).ToNot(HaveOccurred())
	}

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
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("AfterEach Worker %d: Test cleanup completed\n", workerID)
})
