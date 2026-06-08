package multiorg_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/auxiliary"
	"github.com/flightctl/flightctl/test/e2e/infra/quadlet"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const quadletOrgName = "multiorg-test"

// quadletSharedOrgID holds the org ID for the shared test organization on quadlet.
// On quadlet, cluster-admin users default to the system org (00000000...),
// so we capture the real org ID from a non-admin user and use it to switch
// admin into the correct org context during tests.
var quadletSharedOrgID string

func TestMultiorg(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Multiorg E2E Suite")
}

// quadletRoleName maps short role names to PAM group names.
func quadletRoleName(role string) string {
	switch role {
	case "admin":
		return quadlet.RoleAdmin
	case "operator":
		return quadlet.RoleOperator
	case "viewer":
		return quadlet.RoleViewer
	case "installer":
		return quadlet.RoleInstaller
	default:
		return "flightctl-" + role
	}
}

// testUserCreds returns the user credentials adapted for the current environment.
// On quadlet, usernames are prefixed with "multiorg-" to avoid conflicting with
// pre-existing system users.
func testUserCreds() []userCred {
	if infra.IsQuadletEnvironment() {
		return []userCred{
			{"multiorg-admin", "multiorg-admin"},
			{"multiorg-operator", "multiorg-operator"},
			{"multiorg-viewer", "multiorg-viewer"},
			{"multiorg-installer", "multiorg-installer"},
		}
	}
	return []userCred{
		{adminUser, adminPass},
		{operatorUser, operatorPass},
		{viewerUser, viewerPass},
		{installerUser, installerPass},
	}
}

// rbacTestUsers returns the RBAC user definitions for EnsureTestUsers.
func rbacTestUsers() []login.TestUser {
	creds := testUserCreds()
	roles := []string{"admin", "operator", "viewer", "installer"}
	users := make([]login.TestUser, len(creds))
	for i, c := range creds {
		users[i] = login.TestUser{Name: c.name, Role: roles[i]}
	}
	return users
}

var _ = BeforeSuite(func() {
	_, _, err := e2e.SetupWorkerHarnessWithoutVM()
	Expect(err).ToNot(HaveOccurred())

	env := infra.DetectEnvironment()
	if env == infra.EnvironmentKind {
		Skip("Multiorg tests require multi-user auth (not available on KIND)")
	}

	auxiliary.Get(context.Background())
	Expect(setup.EnsureDefaultProviders(nil)).To(Succeed())

	harness := e2e.GetWorkerHarness()
	flightCtlNs := os.Getenv("FLIGHTCTL_NS")
	users := rbacTestUsers()

	switch env {
	case infra.EnvironmentOCP:
		if flightCtlNs == "" {
			Skip("FLIGHTCTL_NS environment variable must be set for OCP multiorg tests")
		}

		defaultK8sContext, ctxErr := harness.GetDefaultK8sContext()
		Expect(ctxErr).ToNot(HaveOccurred(), "Failed to get default K8s context")
		_, ctxErr = harness.SH("kubectl", "config", "use-context", defaultK8sContext)
		Expect(ctxErr).ToNot(HaveOccurred(), "Failed to switch to default K8s context")
		ctxErr = harness.RefreshCluster()
		Expect(ctxErr).ToNot(HaveOccurred(), "Failed to refresh Kubernetes client")

		kubeadminPass := os.Getenv("KUBEADMIN_PASS")
		if kubeadminPass == "" {
			Skip("KUBEADMIN_PASS must be set for OCP multiorg RBAC setup")
		}

		err = login.Login(harness, "kubeadmin", kubeadminPass)
		Expect(err).ToNot(HaveOccurred(), "Failed to login as kubeadmin")
		err = harness.RefreshCluster()
		Expect(err).ToNot(HaveOccurred(), "Failed to refresh k8s client after kubeadmin login")

	case infra.EnvironmentQuadlet:
		providers := setup.GetDefaultProviders()
		Expect(providers).ToNot(BeNil(), "Providers must be initialized")
		pamRBAC, ok := providers.RBAC.(*quadlet.PAMRBACProvider)
		Expect(ok).To(BeTrue(), "Expected PAMRBACProvider on quadlet environment")

		for _, u := range users {
			pamRole := quadletRoleName(u.Role)
			setupErr := pamRBAC.SetupTestUser(u.Name, u.Name, pamRole, quadletOrgName)
			Expect(setupErr).ToNot(HaveOccurred(),
				fmt.Sprintf("Failed to setup PAM test user %s with role %s", u.Name, pamRole))
			GinkgoWriter.Printf("Created PAM test user %s with role %s in org %s\n",
				u.Name, pamRole, quadletOrgName)
		}
	}

	err = login.EnsureTestUsers(harness, flightCtlNs, users)
	Expect(err).ToNot(HaveOccurred())

	creds := testUserCreds()
	for _, u := range creds {
		GinkgoWriter.Printf("Verifying login for user %s\n", u.name)
		loginErr := login.Login(harness, u.name, u.password)
		Expect(loginErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to login as %s", u.name))

		orgID, orgErr := harness.GetOrganizationID()
		Expect(orgErr).ToNot(HaveOccurred(), fmt.Sprintf("Failed to get organization for %s", u.name))
		GinkgoWriter.Printf("User %s has access to organization: %s\n", u.name, orgID)
	}

	// On quadlet, capture the shared org ID from a non-admin user.
	// Cluster-admin users default to the system org; we need the real org ID
	// to switch admin into the correct context during tests.
	if infra.IsQuadletEnvironment() {
		operatorCreds := creds[1]
		loginErr := login.Login(harness, operatorCreds.name, operatorCreds.password)
		Expect(loginErr).ToNot(HaveOccurred(), "Failed to login as operator for org ID capture")
		orgID, orgErr := harness.GetOrganizationID()
		Expect(orgErr).ToNot(HaveOccurred(), "Failed to get org ID from operator")
		quadletSharedOrgID = orgID
		GinkgoWriter.Printf("Captured quadlet shared org ID: %s\n", quadletSharedOrgID)
	}

	adminCreds := creds[0]
	err = loginAndSetOrg(harness, adminCreds.name, adminCreds.password)
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

	// Capture logs if test failed
	harness.PrintAgentLogsIfFailed()
	harness.CaptureDeploymentLogsIfFailed()

	creds := testUserCreds()
	_ = loginAndSetOrg(harness, creds[0].name, creds[0].password)

	err := harness.CleanUpAllTestResources()
	Expect(err).ToNot(HaveOccurred())

	harness.SetTestContext(suiteCtx)

	GinkgoWriter.Printf("[AfterEach] Worker %d: Multiorg test cleanup completed\n", workerID)
})

var _ = AfterSuite(func() {
	harness := e2e.GetWorkerHarness()
	env := infra.DetectEnvironment()

	switch env {
	case infra.EnvironmentOCP:
		flightCtlNs := os.Getenv("FLIGHTCTL_NS")
		if flightCtlNs != "" {
			_ = login.CleanupTestUsers(harness, flightCtlNs, rbacTestUsers())
		}

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

	case infra.EnvironmentQuadlet:
		providers := setup.GetDefaultProviders()
		if providers == nil || providers.RBAC == nil {
			GinkgoWriter.Println("Warning: no providers available for quadlet cleanup")
			return
		}
		pamRBAC, ok := providers.RBAC.(*quadlet.PAMRBACProvider)
		if !ok {
			GinkgoWriter.Println("Warning: RBAC provider is not PAMRBACProvider")
			return
		}
		for _, u := range rbacTestUsers() {
			if err := pamRBAC.CleanupTestUser(u.Name); err != nil {
				GinkgoWriter.Printf("Warning: failed to cleanup PAM user %s: %v\n", u.Name, err)
			}
		}
		_ = pamRBAC.DeleteOrganization(context.Background(), quadletOrgName)
		GinkgoWriter.Println("AfterSuite: cleaned up PAM test users and org")
	}
})
