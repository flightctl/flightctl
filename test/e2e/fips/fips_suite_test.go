package fips_test

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

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

	installConfig, err := getClusterInstallConfig()
	if err != nil {
		Skip("could not read cluster-config-v1: " + err.Error())
	}
	if !strings.Contains(strings.ToLower(installConfig), "fips: true") {
		Skip("FIPS suite requires a cluster installed with FIPS enabled (fips: true in install-config)")
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

func getClusterInstallConfig() (string, error) {
<<<<<<< HEAD
	out, err := exec.Command("oc", "get", "cm", "cluster-config-v1", "-n", "kube-system", "-o", "jsonpath={.data.install-config}").CombinedOutput()
=======
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "oc", "get", "cm", "cluster-config-v1", "-n", "kube-system", "-o", "jsonpath={.data.install-config}").CombinedOutput()
>>>>>>> 9225615f (EDM-3877: Added fips check on BeforeSuite to avoid run on non fips)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())
})
