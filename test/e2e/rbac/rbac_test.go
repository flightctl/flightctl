package rbac_test

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// Default value for the io.flightctl/instance label
	defaultOrgLabel = "flightctl"
	// Environment variable to override the org label value
	orgLabelEnvVar = "FLIGHTCTL_ORG_LABEL"
	// Label key for the org label
	orgLabelKey = "io.flightctl/instance"
)

// getInstanceValue returns the instance value from environment variable or default
func getInstanceValue() string {
	if value := os.Getenv(orgLabelEnvVar); value != "" {
		return value
	}
	return defaultOrgLabel
}

const (
	nonAdminUser         = "demouser1"
	adminRoleName        = "rbac-test-admin-role"
	adminRoleBindingName = "rbac-test-admin-role-binding"
	userRoleName         = "rbac-test-user-role"
	userRoleBindingName  = "rbac-test-user-role-binding"
)

type testResources struct {
	resources     []string
	shouldSucceed bool
}

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
		defaultK8sContext, err = harness.GetDefaultK8sContext()
		Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
		k8sApiEndpoint, err = harness.GetK8sApiEndpoint(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")

		// Create two test namespaces with unique names
		testID := harness.GetTestIDFromContext()
		testNs1 = fmt.Sprintf("rbac-test-ns1-%s", testID)
		testNs2 = fmt.Sprintf("rbac-test-ns2-%s", testID)

		By(fmt.Sprintf("Creating test namespaces: %s and %s", testNs1, testNs2))
		orgLabelValue := getInstanceValue()
		ns1 := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNs1,
				Labels: map[string]string{
					orgLabelKey: orgLabelValue,
				},
			},
		}
		ns2 := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: testNs2,
				Labels: map[string]string{
					orgLabelKey: orgLabelValue,
				},
			},
		}

		_, err = harness.Cluster.CoreV1().Namespaces().Create(suiteCtx, ns1, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred(), "Failed to create first test namespace")
		GinkgoWriter.Printf("Created test namespace: %s\n", testNs1)

		_, err = harness.Cluster.CoreV1().Namespaces().Create(suiteCtx, ns2, metav1.CreateOptions{})
		Expect(err).ToNot(HaveOccurred(), "Failed to create second test namespace")
		GinkgoWriter.Printf("Created test namespace: %s\n", testNs2)

		// Set namespace context to testNs1 for RBAC operations
		By(fmt.Sprintf("Setting namespace context to %s", testNs1))
		err = harness.ChangeK8sNamespace(testNs1)
		Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Failed to change namespace to %s: %v", testNs1, err))

		// Grant the non-admin users the "view" role in both test namespaces
		By(fmt.Sprintf("Granting %s and %s view role in test namespaces", nonAdminUser, nonAdminUser))
		viewRoleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "view-role-binding",
				Namespace: testNs1,
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "ClusterRole",
				Name:     "view",
			},
			Subjects: []rbacv1.Subject{
				{
					Kind: "User",
					Name: nonAdminUser,
				},
				{
					Kind: "User",
					Name: nonAdminUser,
				},
			},
		}

		_, err = harness.CreateRoleBinding(suiteCtx, harness.Cluster, testNs1, viewRoleBinding)
		Expect(err).ToNot(HaveOccurred(), "Failed to create view role binding in first test namespace")
		GinkgoWriter.Printf("Granted demouser1 view role in namespace: %s\n", testNs1)

		viewRoleBinding.Namespace = testNs2
		_, err = harness.CreateRoleBinding(suiteCtx, harness.Cluster, testNs2, viewRoleBinding)
		Expect(err).ToNot(HaveOccurred(), "Failed to create view role binding in second test namespace")
		GinkgoWriter.Printf("Granted demouser1 view role in namespace: %s\n", testNs2)

		By("Waiting for RBAC changes to propagate")
		time.Sleep(2 * time.Second)
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
		err = harness.Cluster.CoreV1().Namespaces().Delete(suiteCtx, testNs1, metav1.DeleteOptions{})
		if err != nil {
			GinkgoWriter.Printf("Warning: Failed to delete test namespace %s: %v\n", testNs1, err)
		} else {
			GinkgoWriter.Printf("Deleted test namespace: %s\n", testNs1)
		}

		err = harness.Cluster.CoreV1().Namespaces().Delete(suiteCtx, testNs2, metav1.DeleteOptions{})
		if err != nil {
			GinkgoWriter.Printf("Warning: Failed to delete test namespace %s: %v\n", testNs2, err)
		} else {
			GinkgoWriter.Printf("Deleted test namespace: %s\n", testNs2)
		}
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
			err := login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail without a role")
			operations := []string{e2e.OperationCreate, e2e.OperationList}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, userTestLabels, testNs1, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Creating an admin role and a role binding")
			// Use testNs1 for RBAC operations
			adminRole.Namespace = testNs1
			createdAdminRole, err := harness.CreateRole(suiteCtx, harness.Cluster, testNs1, adminRole)
			Expect(err).ToNot(HaveOccurred())
			adminRoleBinding.Namespace = testNs1
			createdAdminRoleBinding, err := harness.CreateRoleBinding(suiteCtx, harness.Cluster, testNs1, adminRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should succeed with admin role")
			// Change namespace context to testNs1 so all operations are performed in the correct namespace
			err = harness.ChangeK8sNamespace(testNs1)
			Expect(err).ToNot(HaveOccurred(), "Failed to change namespace to testNs1")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
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
			err = harness.DeleteRole(suiteCtx, harness.Cluster, testNs1, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, testNs1, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Creating an admin role and a role binding in the second test namespace")
			adminRoleDefault := adminRole.DeepCopy()
			adminRoleDefault.Namespace = testNs2
			createdAdminRole, err = harness.CreateRole(suiteCtx, harness.Cluster, testNs2, adminRoleDefault)
			Expect(err).ToNot(HaveOccurred())
			adminRoleBindingDefault := adminRoleBinding.DeepCopy()
			adminRoleBindingDefault.Namespace = testNs2
			createdAdminRoleBinding, err = harness.CreateRoleBinding(suiteCtx, harness.Cluster, testNs2, adminRoleBindingDefault)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail with admin role in the default namespace")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Println("Here!")
			operations = []string{e2e.OperationCreate, e2e.OperationList}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, adminTestLabels, testNs2, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Deleting the admin role and role binding in the default namespace")
			err = harness.DeleteRole(suiteCtx, harness.Cluster, testNs2, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, testNs2, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Creating an admin cluster role and a cluster role binding")
			createdAdminClusterRole, err := harness.CreateClusterRole(suiteCtx, harness.Cluster, adminClusterRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminClusterRoleBinding, err := harness.CreateClusterRoleBinding(suiteCtx, harness.Cluster, adminClusterRoleBinding)
			Expect(err).ToNot(HaveOccurred())
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should succeed with an admin cluster role")
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, testNs2, operations)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the admin cluster role and cluster role binding")
			err = harness.DeleteClusterRole(suiteCtx, harness.Cluster, createdAdminClusterRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete cluster role")
			err = harness.DeleteClusterRoleBinding(suiteCtx, harness.Cluster, createdAdminClusterRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete cluster role binding")
		})
		It("should have a limited access with a non-admin role", Label("84169"), func() {
			By("Login to the cluster as a user without a role")
			err := login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail without a role")
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device", "fleet"}, shouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Creating a role and a role binding")
			userRole.Namespace = testNs1
			createdRole, err := harness.CreateRole(suiteCtx, harness.Cluster, testNs1, userRole)
			Expect(err).ToNot(HaveOccurred())
			userRoleBinding.Namespace = testNs1
			createdRoleBinding, err := harness.CreateRoleBinding(suiteCtx, harness.Cluster, testNs1, userRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that device operations should succeed with the user role")
			// Change namespace context to testNs1 so all operations are performed in the correct namespace
			err = harness.ChangeK8sNamespace(testNs1)
			Expect(err).ToNot(HaveOccurred(), "Failed to change namespace to testNs1")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device"}, shouldSucceed: true},
				{resources: []string{"fleet"}, shouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user role allowing operations on fleet")
			// Ensure namespace context is set to testNs1 before login
			err = harness.ChangeK8sNamespace(testNs1)
			Expect(err).ToNot(HaveOccurred(), "Failed to change namespace to testNs1")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			createdRole.Rules = userExtendedRules
			_, err = harness.UpdateRole(suiteCtx, harness.Cluster, testNs1, createdRole)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user role")
			// Ensure namespace context is set to testNs1 before login
			err = harness.ChangeK8sNamespace(testNs1)
			Expect(err).ToNot(HaveOccurred(), "Failed to change namespace to testNs1")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device", "fleet"}, shouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the role and role binding")
			err = harness.DeleteRole(suiteCtx, harness.Cluster, testNs1, createdRole.Name)
			Expect(err).ToNot(HaveOccurred(), "role should be deleted")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, testNs1, createdRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "role binding should be deleted")

			By("Creating a user cluster role and a cluster role binding")
			createdUserClusterRole, err := harness.CreateClusterRole(suiteCtx, harness.Cluster, userClusterRole)
			Expect(err).ToNot(HaveOccurred())
			createdUserClusterRoleBinding, err := harness.CreateClusterRoleBinding(suiteCtx, harness.Cluster, userClusterRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that device operations should succeed with the user cluster role and fleet operations should fail")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device"}, shouldSucceed: true},
				{resources: []string{"fleet"}, shouldSucceed: false},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user cluster role allowing operations on fleet")
			createdUserClusterRole.Rules = userExtendedRules
			_, err = harness.UpdateClusterRole(suiteCtx, harness.Cluster, createdUserClusterRole)
			Expect(err).ToNot(HaveOccurred())
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user cluster role")
			err = login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device", "fleet"}, shouldSucceed: true},
			}, userTestLabels, testNs1)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the user cluster role and cluster role binding")
			err = harness.DeleteClusterRole(suiteCtx, harness.Cluster, createdUserClusterRole.Name)
			Expect(err).ToNot(HaveOccurred(), "cluster role should be deleted")
			err = harness.DeleteClusterRoleBinding(suiteCtx, harness.Cluster, createdUserClusterRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "cluster role binding should be deleted")

		})
	})
})

// testDeviceFleetOperations Checks that the operations are successful or not for the given resources
func testDeviceFleetOperations(harness *e2e.Harness, suiteCtx context.Context, operations []string, testResources []testResources, userTestLabels *map[string]string, namespace string) error {
	GinkgoWriter.Println("Testing device fleet operations in namespace:", namespace)
	var err error
	for _, testResource := range testResources {
		err = e2e.ExecuteResourceOperations(suiteCtx, harness, testResource.resources, testResource.shouldSucceed, userTestLabels, namespace, operations)
		if err != nil {
			return err
		}
	}
	return nil
}
