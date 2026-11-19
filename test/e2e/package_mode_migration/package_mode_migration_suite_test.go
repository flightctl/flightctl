//go:build linux

package packagemodemigration_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	packagemodemigration "github.com/flightctl/flightctl/test/e2e/package_mode_migration"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var migrationAuxSvcs *auxiliary.Services

// TestPackageModeMigration is the Ginkgo entrypoint for the mixed OS-mode fleet
// template migration e2e suite.
func TestPackageModeMigration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Package Mode Template Migration E2E Suite")
}

var _ = BeforeSuite(func() {
	// This suite mutates deployment-wide state, so it stays inert unless the
	// deployment was explicitly offered to it. The check runs before any other
	// setup so that a wildcard run which happens to include this directory
	// costs nothing and changes nothing. Being opted in is not on its own
	// permission to mutate: the spec re-checks the deployment's contents
	// before it touches anything.
	if !packagemodemigration.OptedIn(os.Getenv(packagemodemigration.ExclusivityEnvVar)) {
		Skip(fmt.Sprintf(
			"suite needs a disposable deployment it owns outright; set %s=true to run it",
			packagemodemigration.ExclusivityEnvVar))
	}

	auxFuture := e2e.StartAuxServicesAsync(context.Background())
	defer func() {
		migrationAuxSvcs = auxFuture.Wait()
	}()

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// The suite harness drives the API only. The image-mode member is a pool VM
	// created by the spec, and the package-mode member is a helper container.
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())
	Expect(e2e.AgentConfigDirExists()).To(BeTrue(),
		"agent config dir must exist (bin/agent/etc/flightctl); run make prepare-e2e-test first")
})

var _ = AfterSuite(func() {
	if migrationAuxSvcs != nil {
		migrationAuxSvcs.Cleanup(context.Background())
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
