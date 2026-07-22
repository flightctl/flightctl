package rootless_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Rootless tests require a quadlet-capable VM (e.g. make deploy-quadlets-vm), same as quadlet tests.
// Standard e2e VMs use podman-compose agent and do not support quadlet/rootless applications.

// containerCandidateLabel marks specs that never reboot the device, so BeforeEach below gives
// them a container-backed device instead of a libvirt VM - see the
// container-backed-device-migration plan. The suite's other spec (checkpoint 87844) reboots the
// device mid-test and must stay on a VM.
const containerCandidateLabel = "container-candidate"

func TestRootless(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rootless applications E2E Suite")
}

var _ = BeforeSuite(func() {
	e2e.SetupWorkerHarnessOrAbort()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	var err error
	if slices.Contains(CurrentSpecReport().Labels(), containerCandidateLabel) {
		GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up rootless test with container device from pool\n", workerID)
		err = harness.SetupContainerFromPoolAndStartAgent(workerID)
	} else {
		GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up rootless test with VM from pool\n", workerID)
		err = harness.SetupVMFromPoolAndStartAgent(workerID)
	}
	Expect(err).ToNot(HaveOccurred())
	out, err := harness.VM.RunSSH([]string{"sudo", "systemctl", "is-active", "flightctl-agent"}, nil)
	Expect(err).ToNot(HaveOccurred())
	active := strings.TrimSpace(out.String())
	GinkgoWriter.Printf("[BeforeEach] Worker %d: Rootless test setup completed (flightctl-agent: %s)\n", workerID, active)
	Expect(active).To(Equal("active"), "flightctl-agent should be active after setup")
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up rootless test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Rootless test cleanup completed\n", workerID)
})

func init() {
	SetDefaultEventuallyTimeout(testutil.DURATION_TIMEOUT)
	SetDefaultEventuallyPollingInterval(testutil.EVENTUALLY_POLLING_250)
}
