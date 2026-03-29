package fips_test

import (
	"context"
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFips(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "FIPS E2E Suite")
}

var _ = BeforeSuite(func() {
	auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	// FIPS tests do not require a VM for cluster/repo checks; use harness without VM.
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())

	// FIPS tests require OpenShift with FIPS enabled (see OpenShift FIPS readiness doc section 4.1).
	if env := infra.DetectEnvironment(); env != infra.EnvironmentOCP {
		Skip("FIPS suite requires OpenShift (OCP) deployment with FIPS enabled; current environment: " + env)
	}
	if !testutil.BinaryExistsOnPath("oc") {
		Skip("FIPS suite requires 'oc' on PATH for OpenShift cluster checks")
	}
})

var _ = BeforeEach(func() {
	if os.Getenv("FLIGHTCTL_NS") == "" {
		Skip("FLIGHTCTL_NS environment variable must be set for FIPS tests")
	}

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "login to API with token")
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	harness.SetTestContext(suiteCtx)
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
})
