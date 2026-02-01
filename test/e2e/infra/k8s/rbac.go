// Package k8s provides Kubernetes-specific implementations of the infra providers.
package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// flightctlAPIGroup is the API group for FlightCtl resources.
	flightctlAPIGroup = "flightctl.io"
	// rbacAPIGroup is the API group for RBAC resources.
	rbacAPIGroup = "rbac.authorization.k8s.io"
)

// RBACProvider implements infra.RBACProvider for Kubernetes environments.
type RBACProvider struct {
	client kubernetes.Interface
}

// NewRBACProvider creates a new K8s RBACProvider.
// If client is nil, it will be created from the default kubeconfig.
func NewRBACProvider(client kubernetes.Interface) (*RBACProvider, error) {
	if client != nil {
		return &RBACProvider{client: client}, nil
	}

	// Create client from kubeconfig
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("failed to build kubeconfig: %w", err)
	}

	client, err = kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return &RBACProvider{client: client}, nil
}

// CreateRole creates a Role in the specified namespace.
func (p *RBACProvider) CreateRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}
	if spec.Namespace == "" {
		return errors.New("namespace cannot be empty for Role")
	}

	role := p.buildRole(spec)
	_, err := p.client.RbacV1().Roles(spec.Namespace).Create(ctx, role, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create role %s in namespace %s: %w", spec.Name, spec.Namespace, err)
	}

	logrus.Infof("K8s RBAC: created role %s in namespace %s", spec.Name, spec.Namespace)
	return nil
}

// UpdateRole updates a Role in the specified namespace.
func (p *RBACProvider) UpdateRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}
	if spec.Namespace == "" {
		return errors.New("namespace cannot be empty for Role")
	}

	role := p.buildRole(spec)
	_, err := p.client.RbacV1().Roles(spec.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update role %s in namespace %s: %w", spec.Name, spec.Namespace, err)
	}

	logrus.Infof("K8s RBAC: updated role %s in namespace %s", spec.Name, spec.Namespace)
	return nil
}

// DeleteRole deletes a Role from the specified namespace.
func (p *RBACProvider) DeleteRole(ctx context.Context, namespace, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if namespace == "" {
		return errors.New("namespace cannot be empty")
	}
	if name == "" {
		return errors.New("role name cannot be empty")
	}

	err := p.client.RbacV1().Roles(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete role %s in namespace %s: %w", name, namespace, err)
	}

	logrus.Infof("K8s RBAC: deleted role %s in namespace %s", name, namespace)
	return nil
}

// CreateClusterRole creates a ClusterRole.
func (p *RBACProvider) CreateClusterRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	clusterRole := p.buildClusterRole(spec)
	_, err := p.client.RbacV1().ClusterRoles().Create(ctx, clusterRole, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create clusterRole %s: %w", spec.Name, err)
	}

	logrus.Infof("K8s RBAC: created clusterRole %s", spec.Name)
	return nil
}

// UpdateClusterRole updates a ClusterRole.
func (p *RBACProvider) UpdateClusterRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	clusterRole := p.buildClusterRole(spec)
	_, err := p.client.RbacV1().ClusterRoles().Update(ctx, clusterRole, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update clusterRole %s: %w", spec.Name, err)
	}

	logrus.Infof("K8s RBAC: updated clusterRole %s", spec.Name)
	return nil
}

// DeleteClusterRole deletes a ClusterRole.
func (p *RBACProvider) DeleteClusterRole(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if name == "" {
		return errors.New("clusterRole name cannot be empty")
	}

	err := p.client.RbacV1().ClusterRoles().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete clusterRole %s: %w", name, err)
	}

	logrus.Infof("K8s RBAC: deleted clusterRole %s", name)
	return nil
}

// CreateRoleBinding creates a RoleBinding in the specified namespace.
func (p *RBACProvider) CreateRoleBinding(ctx context.Context, spec *infra.RoleBindingSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}
	if spec.Namespace == "" {
		return errors.New("namespace cannot be empty for RoleBinding")
	}

	binding := p.buildRoleBinding(spec)
	_, err := p.client.RbacV1().RoleBindings(spec.Namespace).Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create roleBinding %s in namespace %s: %w", spec.Name, spec.Namespace, err)
	}

	logrus.Infof("K8s RBAC: created roleBinding %s in namespace %s", spec.Name, spec.Namespace)
	return nil
}

// DeleteRoleBinding deletes a RoleBinding from the specified namespace.
func (p *RBACProvider) DeleteRoleBinding(ctx context.Context, namespace, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if namespace == "" {
		return errors.New("namespace cannot be empty")
	}
	if name == "" {
		return errors.New("roleBinding name cannot be empty")
	}

	err := p.client.RbacV1().RoleBindings(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete roleBinding %s in namespace %s: %w", name, namespace, err)
	}

	logrus.Infof("K8s RBAC: deleted roleBinding %s in namespace %s", name, namespace)
	return nil
}

// CreateClusterRoleBinding creates a ClusterRoleBinding.
func (p *RBACProvider) CreateClusterRoleBinding(ctx context.Context, spec *infra.RoleBindingSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	binding := p.buildClusterRoleBinding(spec)
	_, err := p.client.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create clusterRoleBinding %s: %w", spec.Name, err)
	}

	logrus.Infof("K8s RBAC: created clusterRoleBinding %s", spec.Name)
	return nil
}

// DeleteClusterRoleBinding deletes a ClusterRoleBinding.
func (p *RBACProvider) DeleteClusterRoleBinding(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if name == "" {
		return errors.New("clusterRoleBinding name cannot be empty")
	}

	err := p.client.RbacV1().ClusterRoleBindings().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete clusterRoleBinding %s: %w", name, err)
	}

	logrus.Infof("K8s RBAC: deleted clusterRoleBinding %s", name)
	return nil
}

// CreateNamespace creates a namespace with the given labels.
func (p *RBACProvider) CreateNamespace(ctx context.Context, name string, labels map[string]string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if name == "" {
		return errors.New("namespace name cannot be empty")
	}

	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}

	_, err := p.client.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create namespace %s: %w", name, err)
	}

	logrus.Infof("K8s RBAC: created namespace %s", name)
	return nil
}

// DeleteNamespace deletes a namespace.
func (p *RBACProvider) DeleteNamespace(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if name == "" {
		return errors.New("namespace name cannot be empty")
	}

	err := p.client.CoreV1().Namespaces().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete namespace %s: %w", name, err)
	}

	logrus.Infof("K8s RBAC: deleted namespace %s", name)
	return nil
}

// GetClient returns the underlying Kubernetes client.
// This is useful for tests that need direct access.
func (p *RBACProvider) GetClient() kubernetes.Interface {
	return p.client
}

// buildRole converts RoleSpec to rbacv1.Role.
func (p *RBACProvider) buildRole(spec *infra.RoleSpec) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: spec.Namespace,
		},
		Rules: p.buildPolicyRules(spec.Permissions),
	}
}

// buildClusterRole converts RoleSpec to rbacv1.ClusterRole.
func (p *RBACProvider) buildClusterRole(spec *infra.RoleSpec) *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: spec.Name,
		},
		Rules: p.buildPolicyRules(spec.Permissions),
	}
}

// buildRoleBinding converts RoleBindingSpec to rbacv1.RoleBinding.
func (p *RBACProvider) buildRoleBinding(spec *infra.RoleBindingSpec) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.Name,
			Namespace: spec.Namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacAPIGroup,
			Kind:     "Role",
			Name:     spec.RoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "User",
				Name: spec.Subject,
			},
		},
	}
}

// buildClusterRoleBinding converts RoleBindingSpec to rbacv1.ClusterRoleBinding.
func (p *RBACProvider) buildClusterRoleBinding(spec *infra.RoleBindingSpec) *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: spec.Name,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacAPIGroup,
			Kind:     "ClusterRole",
			Name:     spec.RoleName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: "User",
				Name: spec.Subject,
			},
		},
	}
}

// buildPolicyRules converts Permissions to rbacv1.PolicyRule slice.
func (p *RBACProvider) buildPolicyRules(permissions []infra.Permission) []rbacv1.PolicyRule {
	rules := make([]rbacv1.PolicyRule, 0, len(permissions))
	for _, perm := range permissions {
		rules = append(rules, rbacv1.PolicyRule{
			APIGroups: []string{flightctlAPIGroup},
			Resources: perm.Resources,
			Verbs:     perm.Verbs,
		})
	}
	return rules
}
