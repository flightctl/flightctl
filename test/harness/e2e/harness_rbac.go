package e2e

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/client"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func (h *Harness) CreateRole(ctx context.Context, kubernetesClient kubernetes.Interface, flightCtlNs string, role *rbacv1.Role) (*rbacv1.Role, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if role == nil {
		return nil, errors.New("role parameter cannot be nil")
	}
	if flightCtlNs == "" {
		return nil, errors.New("namespace cannot be empty")
	}

	role, err := kubernetesClient.RbacV1().Roles(flightCtlNs).Create(ctx, role, metav1.CreateOptions{})
	return role, err
}

func (h *Harness) UpdateRole(ctx context.Context, kubernetesClient kubernetes.Interface, flightCtlNs string, role *rbacv1.Role) (*rbacv1.Role, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if role == nil {
		return nil, errors.New("role cannot be nil")
	}
	if flightCtlNs == "" {
		return nil, errors.New("namespace cannot be empty")
	}
	role, err := kubernetesClient.RbacV1().Roles(flightCtlNs).Update(ctx, role, metav1.UpdateOptions{})
	return role, err
}

func (h *Harness) CreateClusterRole(ctx context.Context, kubernetesClient kubernetes.Interface, clusterRole *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if clusterRole == nil {
		return nil, errors.New("clusterRole cannot be nil")
	}

	clusterRole, err := kubernetesClient.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
	return clusterRole, err
}

func (h *Harness) UpdateClusterRole(ctx context.Context, kubernetesClient kubernetes.Interface, clusterRole *rbacv1.ClusterRole) (*rbacv1.ClusterRole, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if clusterRole == nil {
		return nil, errors.New("clusterRole cannot be nil")
	}
	clusterRole, err := kubernetesClient.RbacV1().ClusterRoles().Update(ctx, clusterRole, metav1.UpdateOptions{})
	return clusterRole, err
}

func (h *Harness) CreateClusterRoleBinding(ctx context.Context, kubernetesClient kubernetes.Interface, clusterRoleBinding *rbacv1.ClusterRoleBinding) (*rbacv1.ClusterRoleBinding, error) {

	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if clusterRoleBinding == nil {
		return nil, errors.New("clusterRoleBinding cannot be nil")
	}

	clusterRoleBinding, err := kubernetesClient.RbacV1().ClusterRoleBindings().Create(ctx, clusterRoleBinding, metav1.CreateOptions{})
	return clusterRoleBinding, err
}

func (h *Harness) CreateRoleBinding(ctx context.Context, kubernetesClient kubernetes.Interface, flightCtlNs string, roleBinding *rbacv1.RoleBinding) (*rbacv1.RoleBinding, error) {
	if ctx == nil {
		return nil, errors.New("context cannot be nil")
	}
	if roleBinding == nil {
		return nil, errors.New("roleBinding cannot be nil")
	}
	if flightCtlNs == "" {
		return nil, errors.New("namespace cannot be empty")
	}
	roleBinding, err := kubernetesClient.RbacV1().RoleBindings(flightCtlNs).Create(ctx, roleBinding, metav1.CreateOptions{})
	return roleBinding, err
}

func (h *Harness) CleanupRoles(ctx context.Context, kubernetesClient kubernetes.Interface, roles []string, roleBindings []string, flightCtlNs string) {
	for _, role := range roles {
		err := h.DeleteRole(ctx, kubernetesClient, flightCtlNs, role)
		if err != nil {
			logrus.Errorf("Failed to delete role %s: %v", role, err)
		} else {
			logrus.Infof("Deleted role %s", role)
		}
	}
	for _, roleBinding := range roleBindings {
		err := h.DeleteRoleBinding(ctx, kubernetesClient, flightCtlNs, roleBinding)
		if err != nil {
			logrus.Errorf("Failed to delete role binding %s: %v", roleBinding, err)
		} else {
			logrus.Infof("Deleted role binding %s", roleBinding)
		}
	}
}

func (h *Harness) CleanupClusterRoles(ctx context.Context, kubernetesClient kubernetes.Interface, clusterRoles []string, clusterRoleBindings []string) {
	for _, clusterRole := range clusterRoles {
		err := h.DeleteClusterRole(ctx, kubernetesClient, clusterRole)
		if err != nil {
			logrus.Errorf("Failed to delete cluster role %s: %v", clusterRole, err)
		}
	}
	for _, clusterRoleBinding := range clusterRoleBindings {
		err := h.DeleteClusterRoleBinding(ctx, kubernetesClient, clusterRoleBinding)
		if err != nil {
			logrus.Errorf("Failed to delete cluster role binding %s: %v", clusterRoleBinding, err)
		}
	}
}

func (h *Harness) DeleteRole(ctx context.Context, client kubernetes.Interface, namespace string, roleName string) error {
	return client.RbacV1().Roles(namespace).Delete(ctx, roleName, metav1.DeleteOptions{})
}

func (h *Harness) DeleteClusterRole(ctx context.Context, client kubernetes.Interface, clusterRoleName string) error {
	return client.RbacV1().ClusterRoles().Delete(ctx, clusterRoleName, metav1.DeleteOptions{})
}

func (h *Harness) DeleteRoleBinding(ctx context.Context, client kubernetes.Interface, namespace string, roleBindingName string) error {
	return client.RbacV1().RoleBindings(namespace).Delete(ctx, roleBindingName, metav1.DeleteOptions{})
}

func (h *Harness) DeleteClusterRoleBinding(ctx context.Context, client kubernetes.Interface, clusterRoleBindingName string) error {
	return client.RbacV1().ClusterRoleBindings().Delete(ctx, clusterRoleBindingName, metav1.DeleteOptions{})
}

// SetCurrentOrganization sets the organization in the client config file and refreshes the harness client.
// Call after changing namespace/login so subsequent API calls use this org.
func (h *Harness) SetCurrentOrganization(org string) error {
	if org == "" {
		return nil
	}
	configPath, err := client.DefaultFlightctlClientConfigPath()
	if err != nil {
		return fmt.Errorf("failed to get client config path: %w", err)
	}
	cfg, err := client.ParseConfigFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}
	cfg.Organization = org
	if err := cfg.Persist(configPath); err != nil {
		return fmt.Errorf("failed to persist config with organization %q: %w", org, err)
	}
	GinkgoWriter.Printf("Set current organization to: %s\n", org)
	return h.RefreshClient()
}

// GetOrganizationIDForNamespace returns the organization ID whose Spec.ExternalId matches the given namespace (e.g. OpenShift project).
// If none match, returns an error so callers can fall back to GetOrganizationID() if desired.
func (h *Harness) GetOrganizationIDForNamespace(namespace string) (string, error) {
	if namespace == "" {
		return "", fmt.Errorf("namespace is empty")
	}
	resp, err := h.Client.ListOrganizationsWithResponse(h.Context, nil)
	if err != nil {
		return "", err
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("no organizations response")
	}
	for _, org := range resp.JSON200.Items {
		if org.Spec != nil && org.Spec.ExternalId != nil && *org.Spec.ExternalId == namespace {
			if org.Metadata.Name != nil && *org.Metadata.Name != "" {
				return *org.Metadata.Name, nil
			}
		}
	}
	return "", fmt.Errorf("no organization found with externalId (namespace) %q", namespace)
}
