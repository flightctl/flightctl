package rbac_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

const (
	nonAdminUser         = "demouser1"
	adminRoleName        = "rbac-test-admin-role"
	adminRoleBindingName = "rbac-test-admin-role-binding"
)

type flightCtlResource struct {
	ResourceType string
	ResourceName string
}

var _ = Describe("RBAC Authorization Tests", Label("rbac", "authorization"), func() {
	var (
		harness            *e2e.Harness
		suiteCtx           context.Context
		flightCtlResources []flightCtlResource
		defaultK8sContext  string
		k8sApiEndpoint     string
	)

	roles := []string{
		adminRoleName,
	}
	roleBindings := []string{
		adminRoleBindingName,
	}
	adminTestLabels := &map[string]string{"test": "rbac-admin"}

	BeforeEach(func() {
		var err error
		flightCtlResources = []flightCtlResource{}
		// Get the harness and context set up by the suite
		harness = e2e.GetWorkerHarness()
		suiteCtx = e2e.GetWorkerContext()

		// Get the default K8s context
		defaultK8sContext, err = getDefaultK8sContext()
		Expect(err).ToNot(HaveOccurred(), "Failed to get default K8s context")
		k8sApiEndpoint, err = getK8sApiEndpoint(harness, defaultK8sContext)
		Expect(err).ToNot(HaveOccurred(), "Failed to get Kubernetes API endpoint")
	})

	AfterEach(func() {
		changeK8sContext(harness, defaultK8sContext)
		login.LoginToAPIWithToken(harness)
		cleanupRoles(roles, roleBindings, harness, suiteCtx)
	})

	Context("FlightCtl Admin Role", func() {
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

		It("should have full access to all resources and operations", Label("sanity", "83842"), func() {
			By("Creating an admin role and a role binding")
			createdAdminRole, err := createRole(suiteCtx, harness.Cluster, flightCtlNs, adminRole)
			Expect(err).ToNot(HaveOccurred())
			createdAdminRoleBinding, err := createRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, adminRoleBinding)
			Expect(err).ToNot(HaveOccurred())

			By("Login to the cluster by a user without a role")
			err = loginAsNonAdmin(harness, nonAdminUser, nonAdminUser, defaultK8sContext, k8sApiEndpoint)
			Expect(err).ToNot(HaveOccurred())

			for _, resourceType := range []string{"device", "fleet", "repository"} {
				By(fmt.Sprintf("Testing %s operations - admin should have full access", resourceType))
				By(fmt.Sprintf("Testing creating a %s", resourceType))
				resourceName, resourceData, err := createResource(harness, resourceType, flightCtlResources)
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("Testing updating a %s", resourceType))
				updatedResourceData, err := harness.AddLabelsToYAML(string(resourceData), *adminTestLabels)
				Expect(err).ToNot(HaveOccurred())
				_, err = harness.CLIWithStdin(updatedResourceData, "apply", "-f", "-")
				Expect(err).ToNot(HaveOccurred())

				By(fmt.Sprintf("Testing getting a specific %s", resourceType))
				_, err = harness.GetResourcesByName(resourceType, resourceName)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Admin should be able to get specific %s", resourceType))

				By(fmt.Sprintf("Testing listing %s", resourceType))
				_, err = harness.GetResourcesByName(resourceType)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Admin should be able to list %s", resourceType))

				By(fmt.Sprintf("Testing deleting a %s", resourceType))
				_, err = harness.CLI("delete", resourceType, resourceName)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Admin should be able to delete %s", resourceType))
			}

			for _, resourceType := range []string{"enrollmentrequests", "events"} {
				By(fmt.Sprintf("Testing %s operations - admin should have full access", resourceType))
				By(fmt.Sprintf("Testing listing %s", resourceType))
				_, err = harness.GetResourcesByName(resourceType)
				Expect(err).ToNot(HaveOccurred(), fmt.Sprintf("Admin should be able to list %s", resourceType))
			}

			By("Deleting the admin role and role binding")
			changeK8sContext(harness, defaultK8sContext)
			err = deleteRole(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRole.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role")
			err = deleteRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, createdAdminRoleBinding.Name)
			Expect(err).ToNot(HaveOccurred(), "Admin should be able to delete role binding")

			By("Testing listing repositories without a role - should not be able to list")
			_, err = harness.GetResourcesByName("repositories")
			Expect(err).To(HaveOccurred(), "A user without a role should not be able to list repositories")
		})
	})
})

func changeK8sContext(harness *e2e.Harness, k8sContext string) {
	cmd := exec.Command("oc", "config", "use-context", k8sContext)
	output, err := cmd.CombinedOutput()
	if err != nil {
		GinkgoWriter.Printf("❌ Failed to change context to %s: %v\n", k8sContext, err)
	} else {
		GinkgoWriter.Printf("✅ Changed context to %s: %s\n", k8sContext, output)
	}
}

func deleteRoleBinding(ctx context.Context, client kubernetes.Interface, namespace string, roleBindingName string) error {
	return client.RbacV1().RoleBindings(namespace).Delete(ctx, roleBindingName, metav1.DeleteOptions{})
}

func deleteRole(ctx context.Context, client kubernetes.Interface, namespace string, roleName string) error {
	return client.RbacV1().Roles(namespace).Delete(ctx, roleName, metav1.DeleteOptions{})
}

func createRole(ctx context.Context, kubenetesClient kubernetes.Interface, flightCtlNs string, role *rbacv1.Role) (*rbacv1.Role, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if role == nil {
		return nil, errors.New("role parameter cannot be nil")
	}
	if flightCtlNs == "" {
		return nil, errors.New("namespace cannot be empty")
	}

	role, err := kubenetesClient.RbacV1().Roles(flightCtlNs).Create(ctx, role, metav1.CreateOptions{})
	return role, err
}

func createRoleBinding(ctx context.Context, kubenetesClient kubernetes.Interface, flightCtlNs string, roleBinding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if roleBinding == nil {
		return nil, errors.New("roleBinding cannot be nil")
	}
	if flightCtlNs == "" {
		return nil, errors.New("namespace cannot be empty")
	}
	roleBinding, err := kubenetesClient.RbacV1().RoleBindings(flightCtlNs).Create(ctx, roleBinding, metav1.CreateOptions{})
	return roleBinding, err
}

func cleanupRoles(roles []string, roleBindings []string, harness *e2e.Harness, suiteCtx context.Context) {
	/*	for _, resource := range flightCtlResources {
			output, err := harness.CleanUpResource(resource.ResourceType, resource.ResourceName)
			if err != nil {
				logrus.Errorf("Failed to clean up resource %s of resource type %s: %v", resource.ResourceName, resource.ResourceType, err)
			} else {
				GinkgoWriter.Printf("Cleaned up resource %s of resource type %s: %s", resource.ResourceName, resource.ResourceType, output)
			}
		}
	*/
	for _, role := range roles {
		err := deleteRole(suiteCtx, harness.Cluster, flightCtlNs, role)
		if err != nil {
			logrus.Errorf("Failed to delete role %s: %v", role, err)
		} else {
			GinkgoWriter.Printf("Deleted role %s", role)
		}
	}
	for _, roleBinding := range roleBindings {
		err := deleteRoleBinding(suiteCtx, harness.Cluster, flightCtlNs, roleBinding)
		if err != nil {
			logrus.Errorf("Failed to delete role binding %s: %v", roleBinding, err)
		} else {
			GinkgoWriter.Printf("Deleted role binding %s", roleBinding)
		}
	}
}

func createResource(harness *e2e.Harness, resourceType string, flightCtlResources []flightCtlResource) (string, []byte, error) {
	uniqueResourceYAML, err := util.CreateUniqueYAMLFile(resourceType+".yaml", harness.GetTestIDFromContext())
	if err != nil {
		return "", nil, err
	}
	defer util.CleanupTempYAMLFile(uniqueResourceYAML)

	// ManageResource applies the resource to the cluster with test labels
	applyOutput, err := harness.ManageResource("apply", uniqueResourceYAML)
	if err != nil {
		return "", nil, err
	}
	if strings.Contains(applyOutput, "201 Created") || strings.Contains(applyOutput, "200 OK") {
		var resource interface{}
		var resourceName *string

		switch resourceType {
		case "device":
			device := harness.GetDeviceByYaml(uniqueResourceYAML)
			updatedDevice, err := harness.GetDevice(*device.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedDevice
			resourceName = updatedDevice.Metadata.Name
		case "fleet":
			fleet := harness.GetFleetByYaml(uniqueResourceYAML)
			updatedFleet, err := harness.GetFleet(*fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedFleet
			resourceName = updatedFleet.Metadata.Name
		case "repository":
			repo := harness.GetRepositoryByYaml(uniqueResourceYAML)
			updatedRepo, err := harness.GetRepository(*repo.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			resource = updatedRepo
			resourceName = updatedRepo.Metadata.Name
		default:
			return "", nil, fmt.Errorf("Unsupported resource type: %s", resourceType)
		}

		// Remove resourceVersion before marshaling to avoid conflicts
		switch resource := resource.(type) {
		case *v1alpha1.Device:
			resource.Metadata.ResourceVersion = nil
		case *v1alpha1.Fleet:
			resource.Metadata.ResourceVersion = nil
		case *v1alpha1.Repository:
			resource.Metadata.ResourceVersion = nil
		}

		resourceData, err := yaml.Marshal(resource)
		if err != nil {
			return "", nil, err
		}
		return *resourceName, resourceData, nil
	} else {
		GinkgoWriter.Printf("Apply output: %s\n", applyOutput)
		return "", nil, fmt.Errorf("Failed to create a %s", resourceType)
	}
}

func getDefaultK8sContext() (string, error) {
	cmd := exec.Command("kubectl", "config", "get-contexts", "-o", "name")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("Failed to get contexts: %v", err)
	}

	contexts := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, context := range contexts {
		if strings.Contains(context, "default") {
			GinkgoWriter.Printf("🔍 [DEBUG] Found default context: %s\n", context)
			return context, nil
		}
	}
	return "", fmt.Errorf("no context with 'default' in name found")
}

func getK8sApiEndpoint(harness *e2e.Harness, k8sContext string) (string, error) {
	changeK8sContext(harness, k8sContext)
	cmd := exec.Command("bash", "-c", "oc whoami --show-server")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("Failed to get Kubernetes API endpoint: %v", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func loginAsNonAdmin(harness *e2e.Harness, user string, password string, k8sContext string, k8sApiEndpoint string) error {
	loginCommand := fmt.Sprintf("oc login -u %s -p %s %s", user, password, k8sApiEndpoint)
	cmd := exec.Command("bash", "-c", loginCommand)
	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("Failed to login to Kubernetes cluster as non-admin: %v %v", loginCommand, err)
	} else {
		GinkgoWriter.Printf("✅ Logged in to Kubernetes cluster as non-admin: %s", loginCommand)
	}

	method := login.LoginToAPIWithToken(harness)
	Expect(method).ToNot(Equal(login.AuthDisabled))
	if method == login.AuthDisabled {
		return errors.New("Login is disabled")
	}
	return nil
}
