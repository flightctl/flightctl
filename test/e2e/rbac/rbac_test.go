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
	userRoleName         = "rbac-test-user-role"
	userRoleBindingName  = "rbac-test-user-role-binding"
	defaultNs            = "default"
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
	})

	AfterEach(func() {
		_, err := harness.ChangeK8sContext(suiteCtx, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to change K8s context")
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
		userRole := &rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRoleName,
				Namespace: flightCtlNs,
			},
		}
		userRole.Rules = []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"devices"},
				Verbs:     []string{"create", "update", "list"},
			},
		}
		userExtendedRules := []rbacv1.PolicyRule{
			{
				APIGroups: []string{"flightctl.io"},
				Resources: []string{"devices", "fleets"},
				Verbs:     []string{"create", "update", "list"},
			},
		}

		userRoleBinding := &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name:      userRoleBindingName,
				Namespace: flightCtlNs,
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
				Verbs:     []string{"create", "update", "list"},
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
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, userTestLabels, flightCtlNs, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

			By("Creating an admin role and a role binding")
			createdAdminRole, err := harness.CreateRole(suiteCtx, harness.Cluster, flightCtlNs, adminRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminRoleBinding, err := harness.CreateRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, adminRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should succeed with admin role")
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, flightCtlNs, operations)
			Expect(err).ToNot(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, true)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the admin role and role binding")
			output, err := harness.ChangeK8sContext(suiteCtx, defaultK8sContext)
			Expect(output).Should(MatchRegexp(fmt.Sprintf("Switched to context \"%s\"", defaultK8sContext)))
			GinkgoWriter.Println("Output:", output)
			Expect(err).ToNot(HaveOccurred(), "Failed to change K8s context")
			err = harness.DeleteRole(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Creating an admin role and a role binding in the default namespace")
			adminRoleDefault := adminRole.DeepCopy()
			adminRoleDefault.Namespace = defaultNs
			createdAdminRole, err = harness.CreateRole(suiteCtx, harness.Cluster, defaultNs, adminRoleDefault)
			Expect(err).ToNot(HaveOccurred())
			adminRoleBindingDefault := adminRoleBinding.DeepCopy()
			adminRoleBindingDefault.Namespace = defaultNs
			createdAdminRoleBinding, err = harness.CreateRoleBinding(suiteCtx, harness.Cluster, defaultNs, adminRoleBindingDefault)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that operations should fail with admin role in the default namespace")
			operations = []string{e2e.OperationCreate, e2e.OperationList}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, false, adminTestLabels, defaultNs, operations)
			Expect(err).NotTo(HaveOccurred())
			err = e2e.ExecuteReadOnlyResourceOperations(harness, []string{"enrollmentrequests", "events"}, false)
			Expect(err).NotTo(HaveOccurred())

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
			operations = []string{e2e.OperationAll}
			err = e2e.ExecuteResourceOperations(suiteCtx, harness, []string{"device", "fleet", "repository"}, true, adminTestLabels, defaultNs, operations)
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
			}, userTestLabels, flightCtlNs)
			Expect(err).ToNot(HaveOccurred())

			By("Creating a role and a role binding")
			createdRole, err := harness.CreateRole(suiteCtx, harness.Cluster, flightCtlNs, userRole)
			Expect(err).ToNot(HaveOccurred())
			createdRoleBinding, err := harness.CreateRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, userRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that device operations should succeed with the user role")
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device"}, shouldSucceed: true},
				{resources: []string{"fleet"}, shouldSucceed: false},
			}, userTestLabels, flightCtlNs)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user role allowing operations on fleet")
			createdRole.Rules = userExtendedRules
			_, err = harness.UpdateRole(suiteCtx, harness.Cluster, flightCtlNs, createdRole)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user role")
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device", "fleet"}, shouldSucceed: true},
			}, userTestLabels, flightCtlNs)
			Expect(err).ToNot(HaveOccurred())

			By("Deleting the role and role binding")
			err = harness.DeleteRole(suiteCtx, harness.Cluster, flightCtlNs, createdRole.Name)
			Expect(err).ToNot(HaveOccurred(), "role should be deleted")
			err = harness.DeleteRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, createdRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "role binding should be deleted")

			By("Creating a user cluster role and a cluster role binding")
			createdUserClusterRole, err := harness.CreateClusterRole(suiteCtx, harness.Cluster, userClusterRole)
			Expect(err).ToNot(HaveOccurred())
			createdUserClusterRoleBinding, err := harness.CreateClusterRoleBinding(suiteCtx, harness.Cluster, userClusterRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that device operations should succeed with the user cluster role and fleet operations should fail")
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device"}, shouldSucceed: true},
				{resources: []string{"fleet"}, shouldSucceed: false},
			}, userTestLabels, flightCtlNs)
			Expect(err).ToNot(HaveOccurred())

			By("Testing adding a rule to the user cluster role allowing operations on fleet")
			createdUserClusterRole.Rules = userExtendedRules
			_, err = harness.UpdateClusterRole(suiteCtx, harness.Cluster, createdUserClusterRole)
			Expect(err).ToNot(HaveOccurred())

			By("Testing that fleet and device operations should succeed with the user cluster role")
			err = testDeviceFleetOperations(harness, suiteCtx, []string{e2e.OperationCreate, e2e.OperationList}, []testResources{
				{resources: []string{"device", "fleet"}, shouldSucceed: true},
			}, userTestLabels, flightCtlNs)
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
func testDeviceFleetOperations(harness *e2e.Harness, suiteCtx context.Context, operations []string, testResources []testResources, userTestLabels *map[string]string, flightCtlNs string) error {
	var err error
	for _, testResource := range testResources {
		err = e2e.ExecuteResourceOperations(suiteCtx, harness, testResource.resources, testResource.shouldSucceed, userTestLabels, flightCtlNs, operations)
		if err != nil {
			return err
		}
	}
	return nil
}
