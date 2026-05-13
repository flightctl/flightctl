package backup_restore

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
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
//
// Kubernetes (kind split namespaces, OCP single namespace, ACM/MCE hub on OpenShift): set FLIGHTCTL_NS to the
// release/API namespace (e.g. flightctl-external) or rely on pod detection; optional FLIGHTCTL_INTERNAL_NS /
// FLIGHTCTL_EXTERNAL_NS when auto-detection is wrong. E2E_ENVIRONMENT=ocp for OpenShift (including ACM hub).
//
// Quadlet: set E2E_ENVIRONMENT=quadlet (and QUADLET_HOST / SSH vars when remote); FLIGHTCTL_NS is not required.
//
// Skipped when PostgreSQL is external or there is no local DB workload (K8s: no flightctl-db pod in internal or
// external namespace; Quadlet: db.type=external or flightctl-db.service inactive). Override: E2E_EXTERNAL_DATABASE=true.
// Helm: deploy/helm/flightctl README (db.type); Podman: docs/user/installing/configuring-external-database.md.

func TestBackupRestore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backup and Restore E2E Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("FLIGHTCTL_NS") == "" && !infra.IsQuadletEnvironment() {
		Skip("Backup/restore e2e requires FLIGHTCTL_NS (e.g. flightctl-external) on Kubernetes; " +
			"for Quadlet set E2E_ENVIRONMENT=quadlet instead")
	}
	auxSvcs = auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	if reason := backupRestoreExternalDBSkipReason(); reason != "" {
		Skip(reason)
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
		return "Backup/restore e2e skipped: no built-in DB workload for pg_dump (K8s: no flightctl-db pod in " +
			"internal/external namespace — external Postgres / Helm db.type=external; Quadlet: db.type=external or " +
			"flightctl-db.service inactive). See deploy/helm/flightctl README and configuring-external-database.md. EDM-3213."
	}
	return ""
}
