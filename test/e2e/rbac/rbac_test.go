package rbac_test

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

		GinkgoWriter.Printf("Creating test namespaces: %s and %s\n", testNs1, testNs2)
		testNamespaces := []string{testNs1, testNs2}
		for _, nsName := range testNamespaces {
			ns := util.CreateTestNamespace(nsName)
			_, err = harness.Cluster.CoreV1().Namespaces().Create(suiteCtx, ns, metav1.CreateOptions{})
			Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create test namespace %s", nsName))
			GinkgoWriter.Printf("Created test namespace: %s\n", nsName)
		}

		// Set namespace context to testNs1 for RBAC operations
		By(fmt.Sprintf("Setting namespace context to %s", testNs1))
		err = harness.ChangeK8sNamespace(testNs1)
		Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to change namespace to %s: %v", testNs1, err))

		// Grant the non-admin user the "view" role in both test namespaces
		By(fmt.Sprintf("Granting %s view role in test namespaces", nonAdminUser))
		createViewRoleBinding(suiteCtx, harness, nonAdminUser, testNs1)
		createViewRoleBinding(suiteCtx, harness, nonAdminUser, testNs2)
	})

	AfterEach(func() {
		_, err := harness.ChangeK8sContext(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to change K8s context")
		login.LoginToAPIWithToken(harness)

		// Cleanup roles in both test namespaces
		harness.CleanupRoles(suiteCtx, harness.Cluster, roles, roleBindings, testNs1)
		harness.CleanupRoles(suiteCtx, harness.Cluster, roles, roleBindings, testNs2)
		harness.CleanupClusterRoles(suiteCtx, harness.Cluster, clusterRoles, clusterRoleBindings)

		// Delete test namespaces
		By(fmt.Sprintf("Deleting test namespaces: %s and %s", testNs1, testNs2))
		_ = util.DeleteNamespace(suiteCtx, harness.Cluster, testNs1)
		_ = util.DeleteNamespace(suiteCtx, harness.Cluster, testNs2)
	})

	Context("FlightCtl user", func() {
		// Note: adminRole and other role definitions will use testNs1
		// They are defined here but the namespace will be set dynamically in BeforeEach
		adminRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adminRoleName,
				Namespace: "", // Will be set to testNs1 in tests
			},
		}
		adminRole.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		}
		adminRoleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adminRoleBindingName,
				Namespace: "", // Will be set to testNs1 in tests
			},
		}
		adminRoleBinding.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     adminRoleName,
		}
		adminRoleBinding.Subjects = []rbacv1.Subject{
			{
				Kind: "User",
				Name: nonAdminUser,
			},
		}
		adminClusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: adminRoleName,
			},
		}
		adminClusterRole.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
		}
		adminClusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: adminRoleBindingName,
			},
		}
		adminClusterRoleBinding.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     adminRoleName,
		}
		adminClusterRoleBinding.Subjects = []rbacv1.Subject{
			{
				Kind: "User",
				Name: nonAdminUser,
			},
		}
		userRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRoleName,
				Namespace: "", // Will be set to testNs1 in tests
			},
		}
		userRole.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"devices"},
				Verbs:     []string{"create", "update", "list", "get"},
			},
		}
		userExtendedRules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"devices", "fleets"},
				Verbs:     []string{"create", "update", "list", "get"},
			},
		}

		userRoleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRoleBindingName,
				Namespace: "", // Will be set to testNs1 in tests
			},
		}
		userRoleBinding.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     userRoleName,
		}
		userRoleBinding.Subjects = []rbacv1.Subject{
			{
				Kind: "User",
				Name: nonAdminUser,
			},
		}
		userClusterRole := &rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{
				Name: userRoleName,
			},
		}
		userClusterRole.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"devices"},
				Verbs:     []string{"create", "update", "list", "get"},
			},
		}
		userClusterRoleBinding := &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: userRoleBindingName,
			},
		}
		userClusterRoleBinding.RoleRef = rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     userRoleName,
		}
		userClusterRoleBinding.Subjects = []rbacv1.Subject{
			{
				Kind: "User",
				Name: nonAdminUser,
			},
		}

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
			// Use testNs1 for RBAC operations
			createdAdminRole, createdAdminRoleBinding := createRoleAndBinding(suiteCtx, harness, adminRole, adminRoleBinding, testNs1)

			By("Testing that operations should succeed with admin role")
			// Change namespace context to testNs1 so all operations are performed in the correct namespace
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
			deleteRoleAndRoleBinding(suiteCtx, harness, testNs1, createdAdminRole.Name, createdAdminRoleBinding.Name)

			By("Creating an admin role and a role binding in the second test namespace")
			adminRoleDefault := adminRole.DeepCopy()
			adminRoleBindingDefault := adminRoleBinding.DeepCopy()
			createdAdminRole, createdAdminRoleBinding = createRoleAndBinding(suiteCtx, harness, adminRoleDefault, adminRoleBindingDefault, testNs2)

			By("Testing that operations should fail with admin role in the default namespace")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			operations = []string{e2e.OperationCreate, e2e.OperationList}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, adminTestLabels, testNs2, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the admin role and role binding in the default namespace")
			deleteRoleAndRoleBinding(suiteCtx, harness, testNs2, createdAdminRole.Name, createdAdminRoleBinding.Name)

			By("Creating an admin cluster role and a cluster role binding")
			createdAdminClusterRole, createdAdminClusterRoleBinding := createClusterRoleAndBinding(suiteCtx, harness, adminClusterRole, adminClusterRoleBinding)
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that operations should succeed with an admin cluster role")
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, testNs2, operations)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the admin cluster role and cluster role binding")
			deleteClusterRoleAndBinding(suiteCtx, harness, createdAdminClusterRole.Name, createdAdminClusterRoleBinding.Name)
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
			createdRole, createdRoleBinding := createRoleAndBinding(suiteCtx, harness, userRole, userRoleBinding, testNs1)

			By("Testing that device operations should succeed with the user role")
			// Change namespace context to testNs1 so all operations are performed in the correct namespace
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device"}, ShouldSucceed: true},
				{Resources: []string{"fleet"}, ShouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user role allowing operations on fleet")
			// Ensure namespace context is set to testNs1 before login
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			createdRole.Rules = userExtendedRules
			_, err = harness.UpdateRole(suiteCtx, harness.Cluster, testNs1, createdRole)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user role")
			// Ensure namespace context is set to testNs1 before login
			changeNamespaceAndLoginAsNonAdmin(harness, testNs1, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device", "fleet"}, ShouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the role and role binding")
			deleteRoleAndRoleBinding(suiteCtx, harness, testNs1, createdRole.Name, createdRoleBinding.Name)

			By("Creating a user cluster role and a cluster role binding")
			createdUserClusterRole, createdUserClusterRoleBinding := createClusterRoleAndBinding(suiteCtx, harness, userClusterRole, userClusterRoleBinding)

			By("Testing that device operations should succeed with the user cluster role and fleet operations should fail")
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device"}, ShouldSucceed: true},
				{Resources: []string{"fleet"}, ShouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user cluster role allowing operations on fleet")
			createdUserClusterRole.Rules = userExtendedRules
			_, err = harness.UpdateClusterRole(suiteCtx, harness.Cluster, createdUserClusterRole)
			Expect(err).ToNot(HaveOccurred())
			loginAsNonAdmin(harness, nonAdminUser, defaultK8sContext, k8sApiEndpoint)

			By("Testing that fleet and device operations should succeed with the user cluster role")
			err = e2e.TestResourceOperations(suiteCtx, harness, []string{e2e.OperationCreate, e2e.OperationList}, []e2e.ResourceTestConfig{
				{Resources: []string{"device", "fleet"}, ShouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the user cluster role and cluster role binding")
			deleteClusterRoleAndBinding(suiteCtx, harness, createdUserClusterRole.Name, createdUserClusterRoleBinding.Name)

		})
	})
})

// createViewRoleBinding creates a view role binding for the specified user in the given namespace.
func createViewRoleBinding(ctx context.Context, harness *e2e.Harness, userName, namespace string) {
	viewRoleBinding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "view-role-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "view",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "User",
				Name: userName,
			},
		},
	}
	_, err := harness.CreateRoleBinding(ctx, harness.Cluster, namespace, viewRoleBinding)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create view role binding in namespace %s", namespace))
	GinkgoWriter.Printf("Granted %s view role in namespace: %s\n", userName, namespace)
}

// changeNamespaceAndLoginAsNonAdmin changes the Kubernetes namespace, logs in as a non-admin user, and sets the current org to the one that corresponds to the namespace.
func changeNamespaceAndLoginAsNonAdmin(harness *e2e.Harness, namespace, userName, k8sContext, k8sApiEndpoint string) {
	err := harness.ChangeK8sNamespace(namespace)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to change namespace to %s", namespace))
	err = login.LoginAsNonAdmin(harness, userName, userName, k8sContext, k8sApiEndpoint)
	Expect(err).ToNot(HaveOccurred())
	// Use the org that corresponds to this namespace (e.g. OpenShift project -> org with matching externalId)
	org, err := harness.GetOrganizationIDForNamespace(namespace)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to get organization for namespace %s", namespace))
	err = harness.SetCurrentOrganization(org)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to set current organization to %s", org))
}

// createRoleAndBinding creates a role and role binding in the specified namespace.
// It returns the created role and role binding.
func createRoleAndBinding(ctx context.Context, harness *e2e.Harness, role *rbacv1.Role, roleBinding *rbacv1.RoleBinding, namespace string) (*rbacv1.Role, *rbacv1.RoleBinding) {
	role.Namespace = namespace
	createdRole, err := harness.CreateRole(ctx, harness.Cluster, namespace, role)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create role %s in namespace %s", role.Name, namespace))

	roleBinding.Namespace = namespace
	createdRoleBinding, err := harness.CreateRoleBinding(ctx, harness.Cluster, namespace, roleBinding)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create role binding %s in namespace %s", roleBinding.Name, namespace))

	return createdRole, createdRoleBinding
}

// deleteRoleAndRoleBinding deletes a role and role binding from the specified namespace.
func deleteRoleAndRoleBinding(ctx context.Context, harness *e2e.Harness, namespace, roleName, roleBindingName string) {
	err := harness.DeleteRole(ctx, harness.Cluster, namespace, roleName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete role %s in namespace %s", roleName, namespace))
	err = harness.DeleteRoleBinding(ctx, harness.Cluster, namespace, roleBindingName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete role binding %s in namespace %s", roleBindingName, namespace))
}

// createClusterRoleAndBinding creates a cluster role and cluster role binding.
// It returns the created cluster role and cluster role binding.
func createClusterRoleAndBinding(ctx context.Context, harness *e2e.Harness, clusterRole *rbacv1.ClusterRole, clusterRoleBinding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRole, *rbacv1.ClusterRoleBinding) {
	createdClusterRole, err := harness.CreateClusterRole(ctx, harness.Cluster, clusterRole)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create cluster role %s", clusterRole.Name))

	createdClusterRoleBinding, err := harness.CreateClusterRoleBinding(ctx, harness.Cluster, clusterRoleBinding)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to create cluster role binding %s", clusterRoleBinding.Name))

	return createdClusterRole, createdClusterRoleBinding
}

// deleteClusterRoleAndBinding deletes a cluster role and cluster role binding.
func deleteClusterRoleAndBinding(ctx context.Context, harness *e2e.Harness, clusterRoleName, clusterRoleBindingName string) {
	err := harness.DeleteClusterRole(ctx, harness.Cluster, clusterRoleName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete cluster role %s", clusterRoleName))
	err = harness.DeleteClusterRoleBinding(ctx, harness.Cluster, clusterRoleBindingName)
	Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to delete cluster role binding %s", clusterRoleBindingName))
}

// loginAsNonAdmin logs in as a non-admin user.
func loginAsNonAdmin(harness *e2e.Harness, userName, k8sContext, k8sApiEndpoint string) {
	err := login.LoginAsNonAdmin(harness, userName, userName, k8sContext, k8sApiEndpoint)
	Expect(err).ToNot(HaveOccurred())
}
