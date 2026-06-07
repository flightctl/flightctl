package vm_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestVM(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "VM E2E Suite")
}

var _ = BeforeSuite(func() {
	auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	e2e.SetupWorkerHarnessOrAbort()
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	err := harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	printVMQuadletDiagnosticsIfFailed(harness)
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})

// printVMQuadletDiagnosticsIfFailed captures quadlet and systemd generator state
// from the VM when the test has failed, to aid in diagnosing kube-quadlet issues.
func printVMQuadletDiagnosticsIfFailed(h *e2e.Harness) {
	if !CurrentSpecReport().Failed() {
		return
	}
	if h.VM == nil {
		return
	}
	running, err := h.VM.IsRunning()
	if err != nil || !running {
		return
	}

	cmds := []struct {
		label string
		args  []string
	}{
		{"quadlet files on disk (recursive)", []string{"sudo", "find", "/etc/containers/systemd", "-ls"}},
		{"drop-in dir contents", []string{"sudo", "sh", "-c", "find /etc/containers/systemd -name '*.d' -type d | xargs -I{} sh -c 'echo \"=== {} ===\"; ls -la {}; find {} -type f | xargs -I@ sh -c \"echo \\\"--- @ ---\\\"; cat @\"' 2>/dev/null || echo '(none)'"}},
		{"generator output ALL", []string{"sudo", "sh", "-c", "ls -la /run/systemd/generator/ 2>/dev/null || echo '(none)'"}},
		{"generator output (test-vm)", []string{"sudo", "sh", "-c", "find /run/systemd/generator -name '*test-vm*' -o -name '*kube*' 2>/dev/null | xargs ls -la 2>/dev/null || echo '(none)'"}},
		{"systemctl unit-files (test-vm)", []string{"sudo", "systemctl", "list-unit-files", "--no-pager", "*test-vm*"}},
		{"systemctl units all (test-vm)", []string{"sudo", "systemctl", "list-units", "--all", "--no-pager", "*test-vm*"}},
		{"podman-system-generator journal", []string{"sudo", "journalctl", "-b", "-t", "podman-system-generator", "--no-pager", "-o", "short"}},
		{"quadlet-generator journal (fallback)", []string{"sudo", "journalctl", "-b", "-t", "quadlet-generator", "--no-pager", "-o", "short"}},
		{"podman version", []string{"podman", "version", "--format", "{{.Client.Version}} / {{.Server.Version}}"}},
	}
	for _, c := range cmds {
		out, _ := h.VM.RunSSH(c.args, nil)
		logrus.Infof("[VM-diag] %s:\n%s", c.label, out.String())
	}
}
