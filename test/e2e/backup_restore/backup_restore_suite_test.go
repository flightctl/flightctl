package backup_restore

import (
	"os"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	defaultEventuallyTimeout         = 5 * time.Minute
	defaultEventuallyPollingInterval = 250 * time.Millisecond
)

// Service backup and restore tests (section 4 of the Recover and restore Test Plan, EDM-415).
// Require in-cluster FlightCtl (kind: flightctl-external + flightctl-internal; OCP: single flightctl namespace) and kubectl.
// Namespaces are detected at runtime. Run with: make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/backup_restore

func TestBackupRestore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backup and Restore E2E Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("FLIGHTCTL_NS") == "" {
		Skip("Backup/restore e2e requires FLIGHTCTL_NS (e.g. flightctl-external); run with in-cluster e2e")
	}
	_, _, err := e2e.SetupWorkerHarness()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up backup/restore test\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	err := harness.SetupVMFromPoolAndStartAgent(workerID)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Backup/restore test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up backup/restore test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Backup/restore test cleanup completed\n", workerID)
})

func init() {
	SetDefaultEventuallyTimeout(defaultEventuallyTimeout)
	SetDefaultEventuallyPollingInterval(defaultEventuallyPollingInterval)
}
