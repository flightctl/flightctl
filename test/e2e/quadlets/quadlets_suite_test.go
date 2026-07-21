package quadlets_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	defaultEventuallyTimeout         = 5 * time.Minute
	defaultEventuallyPollingInterval = 250 * time.Millisecond
)

// containerCandidateLabel marks specs that never reboot the device, so BeforeEach below gives
// them a container-backed device instead of a libvirt VM - see the
// container-backed-device-migration plan. Only the "Inline quadlets with references and reboot"
// context reboots the device mid-test and must stay on a VM.
const containerCandidateLabel = "container-candidate"

// Quadlet tests require a RHEL device with FlightCtl deployed via quadlets
// (e.g. make deploy-quadlets-vm). Standard e2e VMs use podman-compose agent
// and do not support quadlet applications.

func TestQuadlets(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Quadlets E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()
	auxFuture.Wait()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up quadlet test with VM from pool\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	if slices.Contains(CurrentSpecReport().Labels(), containerCandidateLabel) {
		err = harness.SetupContainerFromPoolAndStartAgent(workerID)
	} else {
		err = harness.SetupVMFromPoolAndStartAgent(workerID)
	}
	Expect(err).ToNot(HaveOccurred())

	out, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "is-active", "flightctl-agent"}, nil)
	Expect(err).ToNot(HaveOccurred())
	active := strings.TrimSpace(out.String())
	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Quadlet test setup completed (flightctl-agent: %s)\n", workerID, active)
	Expect(active).To(Equal("active"), "flightctl-agent should be active after setup")
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up quadlet test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Quadlet test cleanup completed\n", workerID)
})

func init() {
	SetDefaultEventuallyTimeout(defaultEventuallyTimeout)
	SetDefaultEventuallyPollingInterval(defaultEventuallyPollingInterval)
}
