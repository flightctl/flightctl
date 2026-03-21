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
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())

	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs == "" {
		Skip("FLIGHTCTL_NS environment variable should be set")
	}

	clusterCtx, err := e2e.GetContext()
	Expect(err).ToNot(HaveOccurred())
	if clusterCtx != testutil.OCP {
		Skip("Multiorg tests require OpenShift deployment (not KIND)")
	}

	harness := e2e.GetWorkerHarness()

	defaultK8sContext, err := harness.GetDefaultK8sContext()
	Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
	err = exec.Command("kubectl", "config", "use-context", defaultK8sContext).Run() // #nosec G204
	Expect(err).ToNot(HaveOccurred(), "Failed to switch to default K8s context")
	err = harness.RefreshCluster()
	Expect(err).ToNot(HaveOccurred(), "Failed to refresh Kubernetes client")

	kubeadminPass := os.Getenv("KUBEADMIN_PASS")
	if kubeadminPass == "" {
		Skip("KUBEADMIN_PASS must be set for multiorg RBAC setup")
	}

	err = login.Login(harness, "kubeadmin", kubeadminPass)
	Expect(err).ToNot(HaveOccurred(), "Failed to login as kubeadmin")
	err = harness.RefreshCluster()
	Expect(err).ToNot(HaveOccurred(), "Failed to refresh k8s client after kubeadmin login")

	rbacUsers := []login.TestUser{
		{Name: adminUser, Role: "admin"},
		{Name: operatorUser, Role: "operator"},
		{Name: viewerUser, Role: "viewer"},
		{Name: installerUser, Role: "installer"},
	}
	err = login.EnsureTestUsers(harness, flightCtlNs, rbacUsers)
	Expect(err).ToNot(HaveOccurred())

	for _, u := range testUsers {
		GinkgoWriter.Printf("Verifying login for user %s\n", u.name)
		loginErr := login.Login(harness, u.name, u.password)
		Expect(loginErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to login as %s", u.name))

		orgID, orgErr := harness.GetOrganizationID()
		Expect(orgErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to get organization for %s", u.name))
		GinkgoWriter.Printf("User %s has access to organization: %s\n", u.name, orgID)
	}

	err = login.Login(harness, adminUser, adminPass)
	Expect(err).ToNot(HaveOccurred(), "Failed to restore admin context")
})

var _ = BeforeEach(func() {
	workerID := GinkgoParallelProcess()
	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Setting up multiorg test\n", workerID)

	ctx := testutil.StartSpecTracerForGinkgo(suiteCtx)
	harness.SetTestContext(ctx)

	_, err := harness.SetupDeviceSimulatorAgentConfig(0, 0)
	Expect(err).ToNot(HaveOccurred())

	GinkgoWriter.Printf("[BeforeEach] Worker %d: Multiorg test setup completed\n", workerID)
})

var _ = AfterEach(func() {
	workerID := GinkgoParallelProcess()
	GinkgoWriter.Printf("[AfterEach] Worker %d: Cleaning up multiorg test resources\n", workerID)

	harness := e2e.GetWorkerHarness()
	suiteCtx := e2e.GetWorkerContext()

	_ = login.Login(harness, adminUser, adminPass)

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Multiorg test cleanup completed\n", workerID)
})

var _ = AfterSuite(func() {
	harness := e2e.GetWorkerHarness()
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	if flightCtlNs != "" {
		rbacUsers := []login.TestUser{
			{Name: adminUser, Role: "admin"},
			{Name: operatorUser, Role: "operator"},
			{Name: viewerUser, Role: "viewer"},
			{Name: installerUser, Role: "installer"},
		}
		_ = login.CleanupTestUsers(harness, flightCtlNs, rbacUsers)
	}

	// Re-login as kubeadmin so subsequent test suites start with cluster-admin credentials
	kubeadminPass := os.Getenv("KUBEADMIN_PASS")
	if kubeadminPass == "" {
		GinkgoWriter.Println("Warning: KUBEADMIN_PASS not set, skipping kubeadmin re-login in AfterSuite")
		return
	}
	if err := login.Login(harness, "kubeadmin", kubeadminPass); err != nil {
		GinkgoWriter.Printf("Warning: failed to re-login as kubeadmin in AfterSuite: %v\n", err)
		return
	}
	_ = harness.RefreshCluster()
	GinkgoWriter.Println("AfterSuite: restored kubeadmin context for subsequent test suites")
})
