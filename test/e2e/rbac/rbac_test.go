package rbac_test

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	nonAdminUser         = "demouser1"
	adminRoleName        = "rbac-test-admin-role"
	adminRoleBindingName = "rbac-test-admin-role-binding"
	defaultNs            = "default"
	OperationCreate      = "create"
	OperationUpdate      = "update"
	OperationGet         = "get"
	OperationList        = "list"
	OperationDelete      = "delete"
	OperationAll         = "all"
)

var _ = Describe("RBAC Authorization Tests", Label("rbac", "authorization"), func() {
	var (
		harness           *e2e.Harness
		suiteCtx          context.Context
		defaultK8sContext string
		k8sApiEndpoint    string
	)

	roles := []string{
		adminRoleName,
	}
	roleBindings := []string{
		adminRoleBindingName,
	}
	clusterRoles := []string{
		adminRoleName,
	}
	clusterRoleBindings := []string{
		adminRoleBindingName,
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
		k8sApiEndpoint, err = harness.GetK8sApiEndpoint(defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")
	})

	AfterEach(func() {
		harness.ChangeK8sContext(defaultK8sContext)
		login.LoginToAPIWithToken(harness)

		harness.CleanupRoles(suiteCtx, harness.Cluster, roles, roleBindings, flightCtlNs)
		harness.CleanupClusterRoles(suiteCtx, harness.Cluster, clusterRoles, clusterRoleBindings)
	})

	Context("FlightCtl user", func() {
		adminRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      adminRoleName,
				Namespace: flightCtlNs,
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
				Namespace: flightCtlNs,
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

		It("should have access full access with an admin role", Label("sanity", "83842"), func() {
			By("Login to the cluster as a user without a role")
			err := login.LoginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail without a role")
			operations := []string{OperationCreate, OperationList}
			testResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, userTestLabels, flightCtlNs, operations)
			testReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)

			By("Creating an admin role and a role binding")
			createdAdminRole, err := harness.CreateRole(suiteCtx, harness.Cluster, flightCtlNs, adminRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminRoleBinding, err := harness.CreateRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, adminRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should succeed with admin role")
			operations = []string{OperationAll}
			testResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, flightCtlNs, operations)
			testReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)

			By("Deleting the admin role and role binding")
			harness.ChangeK8sContext(defaultK8sContext)
			err = harness.DeleteRole(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Creating an admin role and a role binding in the default namespace")
			createdAdminRole, err = harness.CreateRole(suiteCtx, harness.Cluster, defaultNs, adminRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminRoleBinding, err = harness.CreateRoleBinding(suiteCtx, harness.Cluster, defaultNs, adminRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail with admin role in the default namespace")
			operations = []string{OperationCreate, OperationList}
			testResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, adminTestLabels, defaultNs, operations)
			testReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)

			By("Deleting the admin role and role binding in the default namespace")
			err = harness.DeleteRole(suiteCtx, harness.Cluster, defaultNs, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, defaultNs, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Creating an admin cluster role and a cluster role binding")
			createdAdminClusterRole, err := harness.CreateClusterRole(suiteCtx, harness.Cluster, adminClusterRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminClusterRoleBinding, err := harness.CreateClusterRoleBinding(suiteCtx, harness.Cluster, adminClusterRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should succeed with an admin cluster role")
			operations = []string{OperationAll}
			testResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, defaultNs, operations)
			testReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)

			By("Deleting the admin cluster role and cluster role binding")
			err = harness.DeleteClusterRole(suiteCtx, harness.Cluster, createdAdminClusterRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete cluster role")
			err = harness.DeleteClusterRoleBinding(suiteCtx, harness.Cluster, createdAdminClusterRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete cluster role binding")
		})
	})
})

// testResourceOperations tests all CRUD operations for the given resource types
// shouldSucceed determines whether the operations are expected to succeed or fail
func testResourceOperations(ctx context.Context, harness *e2e.Harness, resourceTypes []string, shouldSucceed bool, testLabels *map[string]string, namespace string, operations []string) {
	for _, resourceType := range resourceTypes {
		By(fmt.Sprintf("Testing %s operations - should %s", resourceType, map[bool]string{true: "succeed", false: "fail"}[shouldSucceed]))

		// Check if OperationAll is in the operations list
		operationsToExecute := operations
		for _, op := range operations {
			if op == OperationAll {
				operationsToExecute = []string{OperationCreate, OperationUpdate, OperationGet, OperationList, OperationDelete}
				break
			}
		}

		// Execute operations in the order they appear in operationsToExecute
		var resourceName string
		var resourceData []byte
		var err error
		var output string

		for _, operation := range operationsToExecute {
			switch operation {
			case OperationCreate:
				By(fmt.Sprintf("Testing creating a %s", resourceType))
				output, resourceName, resourceData, err = harness.CreateResource(resourceType)
				if shouldSucceed {
					Expect(err).ToNot(HaveOccurred())
				} else {
					Expect(err).To(HaveOccurred(), fmt.Sprintf("Creating %s should fail", resourceType))
					Expect(output).To(ContainSubstring("403"), fmt.Sprintf("Creating %s should fail with error code 403", resourceType))
				}
			case OperationUpdate:
				By(fmt.Sprintf("Testing updating a %s", resourceType))
				updatedResourceData, err := harness.AddLabelsToYAML(string(resourceData), *testLabels)
				Expect(err).ToNot(HaveOccurred())
				if shouldSucceed {
					output, err = harness.CLIWithStdin(updatedResourceData, "apply", "-f", "-")
					Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Update should succeed for %s", resourceType))
				} else {
					Expect(err).To(HaveOccurred(), fmt.Sprintf("Updating %s should fail", resourceType))
					Expect(output).To(ContainSubstring("403"), fmt.Sprintf("Updating %s should fail with error code 403", resourceType))
				}

			case OperationGet:
				By(fmt.Sprintf("Testing getting a specific %s", resourceType))
				output, err = harness.GetResourcesByName(resourceType, resourceName)
				if shouldSucceed {
					Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Getting specific %s should succeed", resourceType))
				} else {
					Expect(err).To(HaveOccurred(), fmt.Sprintf("Getting specific %s should fail", resourceType))
					Expect(output).To(ContainSubstring("403"), fmt.Sprintf("Getting specific %s should fail with error code 403", resourceType))
				}
			case OperationList:
				By(fmt.Sprintf("Testing listing %s", resourceType))
				output, err = harness.GetResourcesByName(resourceType)
				if shouldSucceed {
					Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Listing %s should succeed", resourceType))
				} else {
					Expect(err).To(HaveOccurred(), fmt.Sprintf("Listing %s should fail", resourceType))
					Expect(output).To(ContainSubstring("403"), fmt.Sprintf("Listing %s should fail with error code 403", resourceType))
				}
			case OperationDelete:
				By(fmt.Sprintf("Testing deleting a %s", resourceType))
				output, err = harness.CLI("delete", resourceType, resourceName)
				if shouldSucceed {
					Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Deleting %s should succeed", resourceType))
				} else {
					Expect(err).To(HaveOccurred(), fmt.Sprintf("Deleting %s should fail", resourceType))
					Expect(output).To(ContainSubstring("403"), fmt.Sprintf("Deleting %s should fail with error code 403", resourceType))
				}
			}
		}
	}
}

// testReadOnlyResourceOperations tests read-only operations for the given resource types
// for the given resource types
// shouldSucceed determines whether the operations are expected to succeed or fail
func testReadOnlyResourceOperations(harness *e2e.Harness, resourceTypes []string, shouldSucceed bool) {
	for _, resourceType := range resourceTypes {
		By(fmt.Sprintf("Testing %s operations - should %s", resourceType, map[bool]string{true: "succeed", false: "fail"}[shouldSucceed]))
		By(fmt.Sprintf("Testing listing %s", resourceType))
		_, err := harness.GetResourcesByName(resourceType)
		if shouldSucceed {
			Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Admin should be able to list %s", resourceType))
		} else {
			Expect(err).To(HaveOccurred(), fmt.Sprintf("Listing %s should fail", resourceType))
		}
	}
}
