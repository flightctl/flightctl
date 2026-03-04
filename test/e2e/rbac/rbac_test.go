package rbac_test

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	nonAdminUser         = "demouser1"
	adminRoleName        = "rbac-test-admin-role"
	adminRoleBindingName = "rbac-test-admin-role-binding"
	userRoleName         = "rbac-test-user-role"
	userRoleBindingName  = "rbac-test-user-role-binding"
)

var _ = Describe("RBAC Authorization Tests", Label("rbac", "authorization"), func() {
	var (
		harness           *e2e.Harness
		suiteCtx          context.Context
		defaultK8sContext string
		k8sApiEndpoint    string
		testNs1           string
		testNs2           string
	)

	roles := []string{
		adminRoleName,
		userRoleName,
	}
	roleBindings := []string{
		adminRoleBindingName,
		userRoleBindingName,
	}
	clusterRoles := []string{
		adminRoleName,
		userRoleName,
	}
	clusterRoleBindings := []string{
		adminRoleBindingName,
		userRoleBindingName,
	}
	adminTestLabels := &map[string]string{"test": "rbac-admin"}
	userTestLabels := &map[string]string{"test": "rbac-user"}

	// Permission definitions
	adminPermissions := []infra.Permission{
		{Resources: []string{"*"}, Verbs: []string{"*"}},
	}
	userPermissions := []infra.Permission{
		{Resources: []string{"devices"}, Verbs: []string{"create", "update", "list", "get"}},
	}
	userExtendedPermissions := []infra.Permission{
		{Resources: []string{"devices", "fleets"}, Verbs: []string{"create", "update", "list", "get"}},
	}

	getRBAC := func() infra.RBACProvider {
		p := setup.GetDefaultProviders()
		Expect(p).ToNot(BeNil(), "infra providers required for RBAC tests")
		Expect(p.RBAC).ToNot(BeNil(), "RBAC provider required")
		return p.RBAC
	}

	BeforeEach(func() {
		var err error
		// Get the harness and context set up by the suite
		harness = e2e.GetWorkerHarness()
		suiteCtx = e2e.GetWorkerContext()

		// Get the default K8s context
		defaultK8sContext, err = harness.GetDefaultK8sAdminContext()
		Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
		k8sApiEndpoint, err = harness.GetK8sApiEndpoint(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")

		// Create two test namespaces with unique names
		testID := harness.GetTestIDFromContext()
		testNs1 = fmt.Sprintf("rbac-test-ns1-%s", testID)
		testNs2 = fmt.Sprintf("rbac-test-ns2-%s", testID)

		GinkgoWriter.Printf("Creating test organizations: %s and %s\n", testNs1, testNs2)
		rbac := getRBAC()
		for _, orgName := range []string{testNs1, testNs2} {
			err = rbac.CreateOrganization(suiteCtx, orgName)
			Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create organization %s", orgName))
			GinkgoWriter.Printf("Created organization: %s\n", orgName)
		}
		By(fmt.Sprintf("Adding %s to test organizations", nonAdminUser))
		for _, orgName := range []string{testNs1, testNs2} {
			err = rbac.AddUserToOrg(suiteCtx, orgName, nonAdminUser)
			Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to add %s to organization %s", nonAdminUser, orgName))
		}
	})

	AfterEach(func() {
		_, err := harness.ChangeK8sContext(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to change K8s context")
		login.LoginToAPIWithToken(harness)

		rbac := getRBAC()
		for _, role := range roles {
			_ = rbac.DeleteRole(suiteCtx, testNs1, role)
			_ = rbac.DeleteRole(suiteCtx, testNs2, role)
		}
		for _, roleBinding := range roleBindings {
			_ = rbac.DeleteRoleBinding(suiteCtx, testNs1, roleBinding)
			_ = rbac.DeleteRoleBinding(suiteCtx, testNs2, roleBinding)
		}
		for _, clusterRole := range clusterRoles {
			_ = rbac.DeleteClusterRole(suiteCtx, clusterRole)
		}
		for _, clusterRoleBinding := range clusterRoleBindings {
			_ = rbac.DeleteClusterRoleBinding(suiteCtx, clusterRoleBinding)
		}

		// Delete test organizations
		By(fmt.Sprintf("Deleting test organizations: %s and %s", testNs1, testNs2))
		_ = rbac.DeleteOrganization(suiteCtx, testNs1)
		_ = rbac.DeleteOrganization(suiteCtx, testNs2)
	})

	Context("FlightCtl user", func() {
		It("should have access full access with an admin role", Label("83842"), func() {
			By("Login to the cluster as a user without a role")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that operations should fail without a role")
			operations := []string{e2e.OperationCreate, e2e.OperationList}
			err := e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, userTestLabels, testNs1, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Creating an admin role and a role binding")
			createRoleAndBinding(suiteCtx, getRBAC(), adminRoleName, adminRoleBindingName, testNs1, nonAdminUser, adminPermissions)

			By("Testing that operations should succeed with admin role")
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, testNs1, operations)
			Expect(err).ToNot(HaveOccurred())

			operations = []string{e2e.OperationCreate}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device"}, true, adminTestLabels, testNs1, operations)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the admin role and role binding")
			output, err := harness.ChangeK8sContext(suiteCtx, defaultK8sContext)
			Expect(output).Should(MatchRegexp(fmt.Sprintf("Switched to context \"%s\"", defaultK8sContext)))
			GinkgoWriter.Println("Output:", output)
			Expect(err).ToNot(HaveOccurred(), "Failed to change K8s context")
			deleteRoleAndRoleBinding(suiteCtx, getRBAC(), testNs1, adminRoleName, adminRoleBindingName)

			By("Creating an admin role and a role binding in the second test namespace")
			createRoleAndBinding(suiteCtx, getRBAC(), adminRoleName, adminRoleBindingName, testNs2, nonAdminUser, adminPermissions)

			By("Testing that operations should fail with admin role in the default namespace")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			operations = []string{e2e.OperationCreate, e2e.OperationList}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, adminTestLabels, testNs2, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the admin role and role binding in the default namespace")
			deleteRoleAndRoleBinding(suiteCtx, getRBAC(), testNs2, adminRoleName, adminRoleBindingName)

			By("Creating an admin cluster role and a cluster role binding")
			createClusterRoleAndBinding(suiteCtx, getRBAC(), adminRoleName, adminRoleBindingName, nonAdminUser, adminPermissions)
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that operations should succeed with an admin cluster role")
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, testNs2, operations)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the admin cluster role and cluster role binding")
			deleteClusterRoleAndBinding(suiteCtx, getRBAC(), adminRoleName, adminRoleBindingName)
		})

		It("should have a limited access with a non-admin role", Label("84169"), func() {
			By("Login to the cluster as a user without a role")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that operations should fail without a role")
			err := e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device", "fleet"}, ShouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Creating a role and a role binding")
			createRoleAndBinding(suiteCtx, getRBAC(), userRoleName, userRoleBindingName, testNs1, nonAdminUser, userPermissions)

			By("Testing that device operations should succeed with the user role")
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device"}, ShouldSucceed: true},
				{Resources: []string{"fleet"}, ShouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user role allowing operations on fleet")
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			updatedRoleSpec := &infra.RoleSpec{
				Name:        userRoleName,
				Namespace:   testNs1,
				Permissions: userExtendedPermissions,
			}
			err = getRBAC().UpdateRole(suiteCtx, updatedRoleSpec)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user role")
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device", "fleet"}, ShouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the role and role binding")
			deleteRoleAndRoleBinding(suiteCtx, getRBAC(), testNs1, userRoleName, userRoleBindingName)

			By("Creating a user cluster role and a cluster role binding")
			createClusterRoleAndBinding(suiteCtx, getRBAC(), userRoleName, userRoleBindingName, nonAdminUser, userPermissions)

			By("Testing that device operations should succeed with the user cluster role and fleet operations should fail")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device"}, ShouldSucceed: true},
				{Resources: []string{"fleet"}, ShouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user cluster role allowing operations on fleet")
			updatedClusterRoleSpec := &infra.RoleSpec{
				Name:        userRoleName,
				Permissions: userExtendedPermissions,
			}
			err = getRBAC().UpdateClusterRole(suiteCtx, updatedClusterRoleSpec)
			Expect(err).ToNot(HaveOccurred())
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that fleet and device operations should succeed with the user cluster role")
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device", "fleet"}, ShouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the user cluster role and cluster role binding")
			deleteClusterRoleAndBinding(suiteCtx, getRBAC(), userRoleName, userRoleBindingName)
		})
	})
})

// changeNamespaceAndLoginAsNonAdmin logs in as a non-admin user and sets the current org to the one that corresponds to the namespace.
func changeNamespaceAndLoginAsNonAdmin(harness *e2e.Harness, namespace, userName, k8sContext, k8sApiEndpoint string) {
	err := login.LoginAsNonAdmin(harness, userName, userName, k8sContext, k8sApiEndpoint)
	Expect(err).ToNot(HaveOccurred())
	// Use the org that corresponds to this namespace (e.g. OpenShift project -> org with matching externalId)
	org, err := harness.GetOrganizationIDForNamespace(namespace)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to get organization for namespace %s", namespace))
	err = harness.SetCurrentOrganization(org)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to set current organization to %s", org))
}

// createRoleAndBinding creates a role and role binding in the specified namespace.
func createRoleAndBinding(ctx context.Context, rbac infra.RBACProvider, roleName, bindingName, namespace, userName string, permissions []infra.Permission) {
	roleSpec := &infra.RoleSpec{
		Name:        roleName,
		Namespace:   namespace,
		Permissions: permissions,
	}
	err := rbac.CreateRole(ctx, roleSpec)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create role %s in namespace %s", roleName, namespace))

	bindingSpec := &infra.RoleBindingSpec{
		Name:      bindingName,
		Namespace: namespace,
		RoleName:  roleName,
		Subject:   userName,
	}
	err = rbac.CreateRoleBinding(ctx, bindingSpec)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create role binding %s in namespace %s", bindingName, namespace))
}

// deleteRoleAndRoleBinding deletes a role and role binding from the specified namespace.
func deleteRoleAndRoleBinding(ctx context.Context, rbac infra.RBACProvider, namespace, roleName, roleBindingName string) {
	err := rbac.DeleteRole(ctx, namespace, roleName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete role %s in namespace %s", roleName, namespace))
	err = rbac.DeleteRoleBinding(ctx, namespace, roleBindingName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete role binding %s in namespace %s", roleBindingName, namespace))
}

// createClusterRoleAndBinding creates a cluster role and cluster role binding.
func createClusterRoleAndBinding(ctx context.Context, rbac infra.RBACProvider, roleName, bindingName, userName string, permissions []infra.Permission) {
	roleSpec := &infra.RoleSpec{
		Name:        roleName,
		Permissions: permissions,
	}
	err := rbac.CreateClusterRole(ctx, roleSpec)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create cluster role %s", roleName))

	bindingSpec := &infra.RoleBindingSpec{
		Name:     bindingName,
		RoleName: roleName,
		Subject:  userName,
	}
	err = rbac.CreateClusterRoleBinding(ctx, bindingSpec)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create cluster role binding %s", bindingName))
}

// deleteClusterRoleAndBinding deletes a cluster role and cluster role binding.
func deleteClusterRoleAndBinding(ctx context.Context, rbac infra.RBACProvider, clusterRoleName, clusterRoleBindingName string) {
	err := rbac.DeleteClusterRole(ctx, clusterRoleName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete cluster role %s", clusterRoleName))
	err = rbac.DeleteClusterRoleBinding(ctx, clusterRoleBindingName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete cluster role binding %s", clusterRoleBindingName))
}

// loginAsNonAdmin logs in as a non-admin user.
func loginAsNonAdmin(harness *e2e.Harness, userName, k8sContext, k8sApiEndpoint string) {
	err := login.LoginAsNonAdmin(harness, userName, userName, k8sContext, k8sApiEndpoint)
	Expect(err).ToNot(HaveOccurred())
}
