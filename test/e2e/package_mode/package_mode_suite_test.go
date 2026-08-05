//go:build linux

package packagemode_test

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

var packageModeAuxSvcs *auxiliary.Services

func TestPackageMode(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Package Mode E2E Suite")
}

var _ = BeforeSuite(func() {
	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	defer func() {
		packageModeAuxSvcs = auxFuture.Wait()
	}()

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	Expect(e2e.AgentConfigDirExists()).To(BeTrue(),
		"agent config dir must exist (bin/agent/etc/flightctl); run make prepare-e2e-test first")
})

var _ = AfterSuite(func() {
	if packageModeAuxSvcs != nil {
		packageModeAuxSvcs.Cleanup(context.Background())
	}
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_, err := login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred())

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()
	defer harness.SetTestContext(suiteCtx)

	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()
})
