package backup_restore

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var auxSvcs *auxiliary.Services

const (
	defaultEventuallyTimeout         = 5 * time.Minute
	defaultEventuallyPollingInterval = 250 * time.Millisecond
)

// Service backup and restore tests (section 4 of the Recover and restore Test Plan, EDM-415).
// Runs on both K8s (kind/OCP) and Quadlet deployments; environment is auto-detected via the infra abstraction.
//
// Skipped when PostgreSQL is external: the backup binary requires a built-in DB workload to run pg_dump via pod exec
// (K8s: no flightctl-db pod; Quadlet: db.type=external in /etc/flightctl/service-config.yaml).
// Override: set E2E_EXTERNAL_DATABASE=true to force skip. EDM-3213.

func TestBackupRestore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backup and Restore E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	// Most specs only exercise backup/restore binaries against the cluster; VM pool is started on demand for needvm specs.
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	auxSvcs = auxFuture.Wait()
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Setting up backup/restore test\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	if slices.Contains(CurrentSpecReport().Labels(), "needvm") {
		err := harness.SetupVMFromPoolAndStartAgent(workerID)
		Expect(err).ToNot(HaveOccurred())
	}

	GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Backup/restore test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Cleaning up backup/restore test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
	Eventually(func() error {
		_, err := login.LoginToAPIWithToken(harness)
		return err
	}, testutil.DURATION_TIMEOUT, testutil.POLLING).Should(Succeed(), "API should be responsive before cleanup")
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Backup/restore test cleanup completed\n", workerID)
})

var _ = AfterSuite(func() {
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

func init() {
	SetDefaultEventuallyTimeout(defaultEventuallyTimeout)
	SetDefaultEventuallyPollingInterval(defaultEventuallyPollingInterval)
}

// backupRestoreExternalDBSkipReason returns a non-empty skip message when this suite cannot run
// (external PostgreSQL or explicit E2E_EXTERNAL_DATABASE).
func backupRestoreExternalDBSkipReason() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("E2E_EXTERNAL_DATABASE"))) {
	case "1", "true", "yes":
		return "Backup/restore e2e skipped: E2E_EXTERNAL_DATABASE set (external DB profile / forced skip). " +
			"These tests require pg_dump via the built-in flightctl-db pod; external DB coverage is tracked under EDM-3213."
	}
	p := setup.GetDefaultProviders()
	if p != nil && p.Infra != nil && !p.Infra.BuiltinDatabaseWorkloadAvailable() {
		return "Backup/restore e2e skipped: no flightctl-db pod (external PostgreSQL / Helm db.type=external). " +
			"Builtin DB uses deploy/helm chart db.type=builtin; external DB guidance: docs/user/installing/configuring-external-database.md. EDM-3213."
	}
	return ""
}
