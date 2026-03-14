package login

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestUser describes a test user with a name and role level.
type TestUser struct {
	Name string // e.g. "demouser1"
	Role string // "admin", "operator", or "viewer"
}

// helmClusterRoleName returns the Helm-generated ClusterRole name for the given
// role level and namespace (e.g. "flightctl-admin-flightctl").
func helmClusterRoleName(role, namespace string) string {
	return fmt.Sprintf("flightctl-%s-%s", role, namespace)
}

// saName returns the ServiceAccount name for a test user (e.g. "flightctl-demouser1").
func saName(user string) string {
	return "flightctl-" + user
}

// roleBindingName returns the RoleBinding name for a test user (e.g. "flightctl-demouser1-binding").
func roleBindingName(user string) string {
	return "flightctl-" + user + "-binding"
}

// EnsureTestUsers provisions test users for the detected cluster type.
// On KIND: creates ServiceAccounts and RoleBindings in the given namespace,
// binding each SA to the appropriate Helm ClusterRole.
// On OCP: creates RoleBindings for pre-existing OCP users (Subject Kind: User).
func EnsureTestUsers(harness *e2e.Harness, namespace string, users []TestUser) error {
	clusterCtx, err := e2e.GetContext()
	if err != nil {
		return fmt.Errorf("detecting cluster context: %w", err)
	}

	ctx := harness.Context
	if ctx == nil {
		ctx = context.Background()
	}

	for _, u := range users {
		clusterRole := helmClusterRoleName(u.Role, namespace)

		if clusterCtx == util.KIND {
			// Create ServiceAccount
			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      saName(u.Name),
					Namespace: namespace,
					Labels: map[string]string{
						"flightctl.service": "flightctl",
						"component":         "test-user",
					},
				},
			}
			_, err := harness.Cluster.CoreV1().ServiceAccounts(namespace).Create(ctx, sa, metav1.CreateOptions{})
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating SA %s: %w", saName(u.Name), err)
			}
			GinkgoWriter.Printf("Created ServiceAccount %s/%s\n", namespace, saName(u.Name))

			// Create RoleBinding to Helm ClusterRole
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleBindingName(u.Name),
					Namespace: namespace,
					Labels: map[string]string{
						"flightctl.service": "flightctl",
						"component":         "test-user",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     clusterRole,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      saName(u.Name),
						Namespace: namespace,
					},
				},
			}
			_, err = harness.Cluster.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{})
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating RoleBinding %s: %w", roleBindingName(u.Name), err)
			}
			GinkgoWriter.Printf("Created RoleBinding %s/%s -> ClusterRole %s\n", namespace, roleBindingName(u.Name), clusterRole)

		} else {
			// OCP: create RoleBinding for pre-existing OCP user
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      roleBindingName(u.Name),
					Namespace: namespace,
					Labels: map[string]string{
						"flightctl.service": "flightctl",
						"component":         "test-user",
					},
				},
				RoleRef: rbacv1.RoleRef{
					APIGroup: "rbac.authorization.k8s.io",
					Kind:     "ClusterRole",
					Name:     clusterRole,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind: "User",
						Name: u.Name,
					},
				},
			}
			_, err := harness.Cluster.RbacV1().RoleBindings(namespace).Create(ctx, rb, metav1.CreateOptions{})
			if err != nil && !k8serrors.IsAlreadyExists(err) {
				return fmt.Errorf("creating RoleBinding %s for OCP user %s: %w", roleBindingName(u.Name), u.Name, err)
			}
			GinkgoWriter.Printf("Created RoleBinding %s/%s for OCP user %s -> ClusterRole %s\n",
				namespace, roleBindingName(u.Name), u.Name, clusterRole)
		}
	}

	return nil
}

// CleanupTestUsers removes ServiceAccounts and RoleBindings created by EnsureTestUsers.
func CleanupTestUsers(harness *e2e.Harness, namespace string, users []TestUser) error {
	clusterCtx, err := e2e.GetContext()
	if err != nil {
		return fmt.Errorf("detecting cluster context: %w", err)
	}

	ctx := harness.Context
	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	for _, u := range users {
		// Delete RoleBinding
		err := harness.Cluster.RbacV1().RoleBindings(namespace).Delete(ctx, roleBindingName(u.Name), metav1.DeleteOptions{})
		if err != nil && !k8serrors.IsNotFound(err) {
			GinkgoWriter.Printf("Warning: failed to delete RoleBinding %s: %v\n", roleBindingName(u.Name), err)
			lastErr = err
		} else {
			GinkgoWriter.Printf("Deleted RoleBinding %s/%s\n", namespace, roleBindingName(u.Name))
		}

		// On KIND, also delete ServiceAccount
		if clusterCtx == util.KIND {
			err = harness.Cluster.CoreV1().ServiceAccounts(namespace).Delete(ctx, saName(u.Name), metav1.DeleteOptions{})
			if err != nil && !k8serrors.IsNotFound(err) {
				GinkgoWriter.Printf("Warning: failed to delete SA %s: %v\n", saName(u.Name), err)
				lastErr = err
			} else {
				GinkgoWriter.Printf("Deleted ServiceAccount %s/%s\n", namespace, saName(u.Name))
			}
		}
	}

	return lastErr
}
