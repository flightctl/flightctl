package encryption_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var auxSvcs *auxiliary.Services

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

	harness := e2e.GetWorkerHarness()
	_, err = login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "bootstrap admin login failed")
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "restore admin login before spec")
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
