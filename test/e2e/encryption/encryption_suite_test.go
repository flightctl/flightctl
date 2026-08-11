package encryption_test

import (
	"context"
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

const (
	// loginRetryTimeout is how long to retry login during recovery restarts.
	// The API pod may report Ready in K8s before the process accepts TLS connections.
	loginRetryTimeout = 2 * time.Minute
	// loginProbeTimeout is how long to retry login at the start of each spec before
	// triggering recovery. Kept short so a crash-loop is detected quickly without
	// burning 2 minutes per spec on a broken cluster.
	loginProbeTimeout = 30 * time.Second
)

var (
	auxSvcs *auxiliary.Services
	// originalServiceConfig is the API service config captured at suite start before
	// any test mutations. The recovery path uses it to restore services to a known-good
	// state without synthesizing an encryption block that may reference key files that
	// don't exist on this deployment.
	originalServiceConfig string
)

func TestEncryption(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Encryption at Rest E2E Suite")
}

var _ = BeforeSuite(func() {
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	ctx := context.Background()

	// Start all needed auxiliary services:
	// - Keycloak for OIDC AuthProvider creation (clientSecret encryption tests)
	// - Git server for SSH Repository credential tests
	// - Prometheus for metrics validation
	var err error
	auxSvcs, err = auxiliary.StartServices(ctx, []auxiliary.Service{
		auxiliary.ServiceKeycloak,
		auxiliary.ServiceGitServer,
		auxiliary.ServicePrometheus,
	})
	Expect(err).ToNot(HaveOccurred(), "failed to start auxiliary services")
	Expect(auxSvcs.Keycloak.URL).ToNot(BeEmpty(), "Keycloak URL must not be empty")

	_, _, err = e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup worker harness")

	// Capture the original API config before any test mutations so the recovery path
	// can restore it exactly — not synthesize an encryption block that may point to
	// key files that don't exist on this deployment.
	originalServiceConfig = captureOriginalServiceConfig()

	harness := e2e.GetWorkerHarness()
	if _, loginErr := login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout); loginErr != nil {
		// The API may be in a crash-loop from a previous test run that left services with a
		// rotated encryption key config that prevents startup. Restore the original config
		// (captured above) and restart so the suite can proceed.
		GinkgoWriter.Printf("BeforeSuite: initial login failed (%v); attempting recovery by restoring original service config\n", loginErr)
		Expect(recoverServicesToOriginalConfig()).To(Succeed(), "recovery: restore original service config")
		Expect(restartServicesAndWait()).To(Succeed(), "recovery: restart services after config restore")
		_, err = login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
		Expect(err).ToNot(HaveOccurred(), "bootstrap admin login failed after recovery")
	}
	// Best-effort cleanup of stable resources that may have been left by a previous crashed run.
	// enc-rot-ap-stable uses the same issuer+clientId as other test specs; if it persists from a
	// prior crash, subsequent tests fail with 409 "issuer and clientId already exists".
	cleanUpStableEncryptionResources(harness)
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Probe with a short timeout first. If the API is down (e.g. a previous test
	// crashed it with a bad encryption config), trigger the same recovery as
	// BeforeSuite rather than waiting 2 minutes per spec.
	if _, loginErr := login.LoginToAPIWithTokenWithRetry(harness, loginProbeTimeout); loginErr != nil {
		GinkgoWriter.Printf("BeforeEach: login probe failed (%v); attempting recovery by restoring original service config\n", loginErr)
		Expect(recoverServicesToOriginalConfig()).To(Succeed(), "recovery: restore original service config")
		Expect(restartServicesAndWait()).To(Succeed(), "recovery: restart services after config restore")
		_, err := login.LoginToAPIWithTokenWithRetry(harness, loginRetryTimeout)
		Expect(err).ToNot(HaveOccurred(), "restore admin login before spec (after recovery)")
	}
	// Always clean up stable resources that may have been left by a previous crashed spec.
	// enc-rot-ap-stable uses the same issuer+clientId as S4 and S8; if it persists after a
	// crash (AfterAll ran with the API down and could not delete it), subsequent specs get
	// a 409 even when the API is healthy and the recovery branch above is not entered.
	cleanUpStableEncryptionResources(harness)
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred(), "cleanup test resources")

	harness.SetTestContext(suiteCtx)
})

var _ = AfterSuite(func() {
	if auxSvcs != nil {
		auxSvcs.Cleanup(context.Background())
	}
})

func init() {
	SetDefaultEventuallyTimeout(testutil.DURATION_TIMEOUT)
	SetDefaultEventuallyPollingInterval(testutil.EVENTUALLY_POLLING_250)
}
