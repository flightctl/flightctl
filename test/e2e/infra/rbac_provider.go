// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"context"
)

// Permission defines access to resources.
// K8s uses Resources and Verbs to build PolicyRules.
// PAM ignores this (permissions are implicit based on role name).
type Permission struct {
	Resources []string
	Verbs     []string
}

// RoleSpec defines a role with permissions.
type RoleSpec struct {
	Name        string
	Namespace   string // empty for cluster-scoped
	Permissions []Permission
}

// RoleBindingSpec binds a subject (user) to a role.
type RoleBindingSpec struct {
	Name      string
	Namespace string // empty for cluster-scoped
	RoleName  string
	Subject   string // username
}

// RBACProvider abstracts RBAC operations for different environments.
// K8s implementations use the RBAC API, PAM implementations use Linux groups.
type RBACProvider interface {
	// CreateRole creates a role.
	// K8s: creates rbacv1.Role in namespace
	// PAM: creates group "<namespace>.<name>" if namespace provided, else "<name>"
	CreateRole(ctx context.Context, spec *RoleSpec) error

	// UpdateRole updates a role.
	// K8s: updates rbacv1.Role
	// PAM: no-op (groups don't have permissions)
	UpdateRole(ctx context.Context, spec *RoleSpec) error

	// DeleteRole deletes a role.
	// K8s: deletes rbacv1.Role
	// PAM: deletes group "<namespace>.<name>" or "<name>"
	DeleteRole(ctx context.Context, namespace, name string) error

	// CreateRoleBinding binds a user to a role.
	// K8s: creates rbacv1.RoleBinding
	// PAM: adds user to group via usermod -aG
	CreateRoleBinding(ctx context.Context, spec *RoleBindingSpec) error

	// DeleteRoleBinding removes a user from a role.
	// K8s: deletes rbacv1.RoleBinding
	// PAM: removes user from group via gpasswd -d
	DeleteRoleBinding(ctx context.Context, namespace, name string) error

	// CreateClusterRole creates a cluster-scoped role.
	// K8s: creates rbacv1.ClusterRole
	// PAM: creates group "<name>" (no namespace prefix)
	CreateClusterRole(ctx context.Context, spec *RoleSpec) error

	// UpdateClusterRole updates a cluster-scoped role.
	// K8s: updates rbacv1.ClusterRole
	// PAM: no-op (groups don't have permissions)
	UpdateClusterRole(ctx context.Context, spec *RoleSpec) error

	// DeleteClusterRole deletes a cluster-scoped role.
	// K8s: deletes rbacv1.ClusterRole
	// PAM: deletes group "<name>"
	DeleteClusterRole(ctx context.Context, name string) error

	// CreateClusterRoleBinding binds a user to a cluster role.
	// K8s: creates rbacv1.ClusterRoleBinding
	// PAM: adds user to group via usermod -aG
	CreateClusterRoleBinding(ctx context.Context, spec *RoleBindingSpec) error

	// DeleteClusterRoleBinding removes a user from a cluster role.
	// K8s: deletes rbacv1.ClusterRoleBinding
	// PAM: removes user from group via gpasswd -d
	DeleteClusterRoleBinding(ctx context.Context, name string) error

	// CreateNamespace creates a namespace.
	// K8s: creates Namespace resource
	// PAM: no-op
	CreateNamespace(ctx context.Context, name string, labels map[string]string) error

	// DeleteNamespace deletes a namespace.
	// K8s: deletes Namespace resource
	// PAM: no-op
	DeleteNamespace(ctx context.Context, name string) error
}
