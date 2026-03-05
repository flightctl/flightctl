package multiorg_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMultiorg(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multiorg E2E Suite")
}

var _ = BeforeSuite(func() {
	// Use SetupWorkerHarnessWithoutVM because this suite uses device simulators, not VMs
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())

	// Require FLIGHTCTL_NS environment variable
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs == "" {
		Skip("FLIGHTCTL_NS environment variable should be set")
	}

	// Verify this is an OCP deployment (multiorg requires real OCP users)
	clusterCtx, err := e2e.GetContext()
	Expect(err).ToNot(HaveOccurred())
	if clusterCtx != testutil.OCP {
		Skip("Multiorg tests require OpenShift deployment (not KIND)")
	}

	harness := e2e.GetWorkerHarness()

	// Restore the default (admin) k8s context to ensure valid credentials.
	// Previous test runs may have left the kubeconfig with expired user tokens.
	defaultK8sContext, err := harness.GetDefaultK8sContext()
	Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
	err = exec.Command("kubectl", "config", "use-context", defaultK8sContext).Run() // #nosec G204
	Expect(err).ToNot(HaveOccurred(), "Failed to switch to default K8s context")
	err = harness.RefreshCluster()
	Expect(err).ToNot(HaveOccurred(), "Failed to refresh Kubernetes client")

	// Validate k8s credentials are valid before attempting RBAC operations
	whoamiOutput, whoamiErr := exec.Command("oc", "whoami").Output()
	if whoamiErr != nil {
		Skip("Kubeconfig credentials are invalid or expired. " +
			"Please re-authenticate as cluster admin before running multiorg tests: " +
			"oc login -u kubeadmin -p <password> <api-endpoint>")
	}
	GinkgoWriter.Printf("Authenticated as: %s\n", string(whoamiOutput))

	// Ensure test users have proper RoleBindings in the flightctl namespace
	testUsers := []login.TestUser{
		{Name: "admin", Role: "admin"},
		{Name: "operator", Role: "operator"},
		{Name: "viewer", Role: "viewer"},
	}
	err = login.EnsureTestUsers(harness, flightCtlNs, testUsers)
	Expect(err).ToNot(HaveOccurred())
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up multiorg test\n", workerID)

	// Create test-specific context for proper tracing
	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	// Setup device simulator agent config (certs and config file)
	_, err := harness.SetupDeviceSimulatorAgentConfig(0, 0)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Multiorg test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up multiorg test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	// Re-login as admin before cleanup to ensure full permissions
	defaultK8sContext, err := harness.GetDefaultK8sContext()
	if err == nil {
		k8sApiEndpoint, endpointErr := harness.GetK8sApiEndpoint(suiteCtx, defaultK8sContext)
		if endpointErr == nil {
			_ = login.LoginAsNonAdmin(harness, "admin", "admin", defaultK8sContext, k8sApiEndpoint)
		}
	}

	err = harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Multiorg test cleanup completed\n", workerID)
})

var _ = AfterSuite(func() {
	harness := e2e.GetWorkerHarness()
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs != "" {
		testUsers := []login.TestUser{
			{Name: "admin", Role: "admin"},
			{Name: "operator", Role: "operator"},
			{Name: "viewer", Role: "viewer"},
			{Name: "installer", Role: "installer"},
		}
		_ = login.CleanupTestUsers(harness, flightCtlNs, testUsers)
	}
})
