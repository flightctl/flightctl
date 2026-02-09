package e2e

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// CreateRole creates a role using the environment-appropriate RBAC provider.
func (h *Harness) CreateRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.CreateRole(ctx, spec)
}

// UpdateRole updates a role using the environment-appropriate RBAC provider.
func (h *Harness) UpdateRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.UpdateRole(ctx, spec)
}

// DeleteRole deletes a role using the environment-appropriate RBAC provider.
func (h *Harness) DeleteRole(ctx context.Context, namespace, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.DeleteRole(ctx, namespace, name)
}

// CreateClusterRole creates a cluster role using the environment-appropriate RBAC provider.
func (h *Harness) CreateClusterRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.CreateClusterRole(ctx, spec)
}

// UpdateClusterRole updates a cluster role using the environment-appropriate RBAC provider.
func (h *Harness) UpdateClusterRole(ctx context.Context, spec *infra.RoleSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.UpdateClusterRole(ctx, spec)
}

// DeleteClusterRole deletes a cluster role using the environment-appropriate RBAC provider.
func (h *Harness) DeleteClusterRole(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.DeleteClusterRole(ctx, name)
}

// CreateRoleBinding creates a role binding using the environment-appropriate RBAC provider.
func (h *Harness) CreateRoleBinding(ctx context.Context, spec *infra.RoleBindingSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.CreateRoleBinding(ctx, spec)
}

// DeleteRoleBinding deletes a role binding using the environment-appropriate RBAC provider.
func (h *Harness) DeleteRoleBinding(ctx context.Context, namespace, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.DeleteRoleBinding(ctx, namespace, name)
}

// CreateClusterRoleBinding creates a cluster role binding using the environment-appropriate RBAC provider.
func (h *Harness) CreateClusterRoleBinding(ctx context.Context, spec *infra.RoleBindingSpec) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if spec == nil {
		return errors.New("spec cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.CreateClusterRoleBinding(ctx, spec)
}

// DeleteClusterRoleBinding deletes a cluster role binding using the environment-appropriate RBAC provider.
func (h *Harness) DeleteClusterRoleBinding(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.DeleteClusterRoleBinding(ctx, name)
}

// CreateNamespace creates a namespace using the environment-appropriate RBAC provider.
func (h *Harness) CreateNamespace(ctx context.Context, name string, labels map[string]string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.CreateNamespace(ctx, name, labels)
}

// DeleteNamespace deletes a namespace using the environment-appropriate RBAC provider.
func (h *Harness) DeleteNamespace(ctx context.Context, name string) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	provider := h.GetRBACProvider()
	if provider == nil {
		return errors.New("RBAC provider not available")
	}

	return provider.DeleteNamespace(ctx, name)
}

// CleanupRoles deletes multiple roles and role bindings.
func (h *Harness) CleanupRoles(ctx context.Context, roles []string, roleBindings []string, namespace string) {
	for _, role := range roles {
		err := h.DeleteRole(ctx, namespace, role)
		if err != nil {
			logrus.Debugf("Failed to delete role %s: %v", role, err)
		} else {
			logrus.Infof("Deleted role %s", role)
		}
	}
	for _, roleBinding := range roleBindings {
		err := h.DeleteRoleBinding(ctx, namespace, roleBinding)
		if err != nil {
			logrus.Debugf("Failed to delete role binding %s: %v", roleBinding, err)
		} else {
			logrus.Infof("Deleted role binding %s", roleBinding)
		}
	}
}

// CleanupClusterRoles deletes multiple cluster roles and cluster role bindings.
func (h *Harness) CleanupClusterRoles(ctx context.Context, clusterRoles []string, clusterRoleBindings []string) {
	for _, clusterRole := range clusterRoles {
		err := h.DeleteClusterRole(ctx, clusterRole)
		if err != nil {
			logrus.Debugf("Failed to delete cluster role %s: %v", clusterRole, err)
		}
	}
	for _, clusterRoleBinding := range clusterRoleBindings {
		err := h.DeleteClusterRoleBinding(ctx, clusterRoleBinding)
		if err != nil {
			logrus.Debugf("Failed to delete cluster role binding %s: %v", clusterRoleBinding, err)
		}
	}
}
