package helm_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestHelm(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Helm E2E Suite")
}

var _ = BeforeSuite(func() {
	auxiliary.Get(context.Background())
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	// Get the harness and context directly - no package-level variables
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up test with VM from pool\n", workerID)

	// Create test-specific context for proper tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)

	// Set the test context in the harness
	harness.SetTestContext(ctx)

	// Setup VM from pool, revert to pristine snapshot, and start agent
	err := harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up test resources\n", workerID)

	// Get the harness and context directly - no shared variables needed
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Always print agent logs for debugging flaky tests
	harness.PrintAgentLogs()

	// Dump greenboot/boot-complete state for debugging
	if harness.VM != nil {
		if running, err := harness.VM.IsRunning(); err == nil && running {
			stdout, _ := harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "boot-complete.target", "--no-pager"}, nil)
			GinkgoWriter.Printf("boot-complete.target status:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "systemctl", "list-dependencies", "boot-complete.target", "--no-pager"}, nil)
			GinkgoWriter.Printf("boot-complete.target dependencies:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "greenboot-healthcheck.service", "--no-pager"}, nil)
			GinkgoWriter.Printf("greenboot-healthcheck.service status:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "greenboot-healthcheck.service", "--no-pager"}, nil)
			GinkgoWriter.Printf("greenboot-healthcheck.service journal:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "journalctl", "-u", "greenboot*", "--no-pager"}, nil)
			GinkgoWriter.Printf("all greenboot journals:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "ls", "-la", "/usr/lib/greenboot/check/required.d/"}, nil)
			GinkgoWriter.Printf("greenboot required checks (/usr/lib):\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "ls", "-la", "/etc/greenboot/check/required.d/"}, nil)
			GinkgoWriter.Printf("greenboot required checks (/etc):\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "cat", "/etc/greenboot/greenboot.conf"}, nil)
			GinkgoWriter.Printf("greenboot.conf:\n%s\n", stdout.String())
			stdout, _ = harness.VM.RunSSH([]string{"sudo", "systemctl", "status", "greenboot-grub2-set-success.service", "--no-pager"}, nil)
			GinkgoWriter.Printf("greenboot-grub2-set-success.service status:\n%s\n", stdout.String())
		}
	}

	// Clean up test resources BEFORE switching back to suite context
	// This ensures we use the correct test ID for resource cleanup
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	// Now restore suite context for any remaining cleanup operations
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
