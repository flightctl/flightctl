package aap_rbac_test

import (
	"os"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestAAPRBAC(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "AAP RBAC E2E Suite")
}

var originalClientConfig *clientConfigSnapshot
var adminClientConfig *clientConfigSnapshot

var _ = BeforeSuite(func() {
	// AAP tests run only on Quadlet deployments
	infra.SkipIfNotQuadlet("AAP RBAC tests require Quadlet deployment with AAP auth configured")

	// Verify AAP environment variables are set
	if skipMessage := checkAAPEnvVars(); skipMessage != "" {
		Skip(skipMessage)
	}

	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	// Use harness without VM (AAP RBAC tests only need API + CLI)
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred(), "failed to setup harness")

	harness := e2e.GetWorkerHarness()

	// Capture original client config for restoration
	originalClientConfig, err = captureClientConfigSnapshot()
	Expect(err).ToNot(HaveOccurred(), "failed to capture original client config")

	// Bootstrap: login as admin and capture admin config
	_, err = login.LoginToAPIWithToken(harness)
	Expect(err).ToNot(HaveOccurred(), "bootstrap login failed")
	adminClientConfig, err = captureClientConfigSnapshot()
	Expect(err).ToNot(HaveOccurred(), "failed to capture admin client config")
})

var _ = BeforeEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Create test-specific context with tracing
	ctx := util.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Restore admin credentials before each test
	err := restoreAdminClientConfig(harness)
	Expect(err).ToNot(HaveOccurred(), "restore admin login before spec")
})

var _ = AfterEach(func() {
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Capture logs if test failed
	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	// Restore suite context
	harness.SetTestContext(suiteCtx)
})

var _ = AfterSuite(func() {
	// Skip cleanup if BeforeSuite was skipped (harness not initialized)
	if originalClientConfig == nil {
		return
	}

	harness := e2e.GetWorkerHarness()

	// Restore admin authentication
	err := restoreAdminClientConfig(harness)
	if err != nil {
		logrus.Warnf("Failed to restore admin login: %v", err)
	}

	// Restore original client config
	if err = restoreClientConfigSnapshot(harness, originalClientConfig); err != nil {
		logrus.Warnf("Failed to restore original client config: %v", err)
	}
})

// checkAAPEnvVars verifies required AAP environment variables are set.
// Returns a skip message if any required variable is missing, empty string if all are present.
func checkAAPEnvVars() string {
	if strings.TrimSpace(os.Getenv("AAP_API_URL")) == "" {
		return "AAP_API_URL not set - AAP RBAC tests require AAP environment"
	}
	// E2E_AAP_TOKEN is set by ocp-flightctl-gotests for login authentication
	// AAP_TOKEN is also set when AAP_API_URL is provided (for authprovider suite)
	if strings.TrimSpace(os.Getenv("E2E_AAP_TOKEN")) == "" && strings.TrimSpace(os.Getenv("AAP_TOKEN")) == "" {
		return "E2E_AAP_TOKEN or AAP_TOKEN not set - AAP RBAC tests require admin token"
	}
	return ""
}

// restoreAdminClientConfig restores the suite's cached admin CLI config.
func restoreAdminClientConfig(harness *e2e.Harness) error {
	if adminClientConfig == nil {
		// If admin config is not initialized, skip restoration
		// This can happen if BeforeSuite skipped due to environment checks
		return nil
	}
	return restoreClientConfigSnapshot(harness, adminClientConfig)
}
