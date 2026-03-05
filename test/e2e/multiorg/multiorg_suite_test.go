package multiorg_test

import (
	"fmt"
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

	// EnsureTestUsers requires cluster-admin privileges (kubeadmin), not a regular OCP user.
	// Log in as kubeadmin before creating RoleBindings, then refresh the k8s client.
	kubeadminPass := os.Getenv("KUBEADMIN_PASS")
	if kubeadminPass == "" {
		Skip("KUBEADMIN_PASS must be set for multiorg RBAC setup")
	}

	suiteCtx := e2e.GetWorkerContext()
	k8sApiEndpoint, err := harness.GetK8sApiEndpoint(suiteCtx, defaultK8sContext)
	Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")

	kubeadminLogin := exec.Command("oc", "login", "-u", "kubeadmin", "-p", kubeadminPass, k8sApiEndpoint) // #nosec G204
	kubeadminLogin.Stdout = GinkgoWriter
	kubeadminLogin.Stderr = GinkgoWriter
	err = kubeadminLogin.Run()
	Expect(err).ToNot(HaveOccurred(), "Failed to login as kubeadmin")
	err = harness.RefreshCluster()
	Expect(err).ToNot(HaveOccurred(), "Failed to refresh k8s client after kubeadmin login")

	whoamiOutput, whoamiErr := exec.Command("oc", "whoami").Output() // #nosec G204
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

	// Log in as each user to ensure they can authenticate and have access
	// to the shared flightctl organization before tests run.
	type userCred struct {
		name     string
		password string
	}
	users := []userCred{
		{"admin", "admin"},
		{"operator", "operator"},
		{"viewer", "viewer"},
	}
	for _, u := range users {
		GinkgoWriter.Printf("Verifying login for user %s\n", u.name)
		loginErr := login.LoginAsNonAdmin(harness, u.name, u.password, defaultK8sContext, k8sApiEndpoint)
		Expect(loginErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to login as %s", u.name))

		orgID, orgErr := harness.GetOrganizationID()
		Expect(orgErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to get organization for %s", u.name))
		GinkgoWriter.Printf("User %s has access to organization: %s\n", u.name, orgID)
	}

	// Switch back to admin context so the suite starts in a known state
	err = login.LoginAsNonAdmin(harness, "admin", "admin", defaultK8sContext, k8sApiEndpoint)
	Expect(err).ToNot(HaveOccurred(), "Failed to restore admin context")
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
