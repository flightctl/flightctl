package rootless_test

import (
	"context"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Rootless tests require a quadlet-capable VM (e.g. make deploy-quadlets-vm), same as quadlet tests.
// Standard e2e VMs use podman-compose agent and do not support quadlet/rootless applications.

func TestRootless(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Rootless applications E2E Suite")
}

var _ = BeforeSuite(func() {
	// This suite pulls quay.io/flightctl-tests/* images (see rootlessAlpineImage,
	// rootlessNginxImage in rootless_test.go), which only resolve because the device's
	// registries.conf remaps them to the local registry (see inject_agent_files_into_qcow.sh)
	// - a container that this suite must create/populate itself via StartAuxServicesAsync,
	// same as containers_suite_test.go/helm_suite_test.go. Previously this suite skipped
	// that call entirely and implicitly relied on some *other* suite in the same shard
	// having already started the registry first; when Ginkgo's LPT scheduler happened to
	// run this suite first in a shard, nothing had created the registry yet, so every
	// device got "connection refused" pulling these images for the suite's whole run (see
	// git history for the registry-health diagnostic that root-caused this).
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	e2e.SetupWorkerHarnessOrAbort()
	auxFuture.Wait()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up rootless test with VM from pool\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	err := harness.SetupVMFromPoolAndStartAgent(workerID)
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
