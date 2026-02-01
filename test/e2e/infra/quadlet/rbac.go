// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// PAMRBACProvider implements infra.RBACProvider for Quadlet environments using PAM-based RBAC.
//
// In Quadlet deployments, Flight Control uses PAM (Pluggable Authentication Modules) for
// authentication and Linux groups for authorization. This maps to the K8s RBAC model as follows:
//
// Role/ClusterRole → Linux group:
//   - Namespaced role: group named "<namespace>.<role>" (e.g., "myorg.admin")
//   - Cluster role: group named "<role>" (e.g., "admin")
//
// RoleBinding/ClusterRoleBinding → User added to group:
//   - usermod -aG <group> <user>
//
// The PAM provider in internal/auth/oidc/pam/provider.go interprets these groups:
//   - "namespace.role" → org-scoped role (namespace:role)
//   - "role" (no dot) → cluster-scoped role
type PAMRBACProvider struct {
	// pamIssuerContainer is the name of the PAM issuer container
	pamIssuerContainer string
	// host is the hostname/IP where Quadlet services are running
	host string
	// sshUser is the SSH user for remote connections (empty for local)
	sshUser string
	// sshKeyPath is the path to SSH private key (optional)
	sshKeyPath string
	// useSudo indicates whether to use sudo for commands
	useSudo bool
}

// NewPAMRBACProvider creates a new PAMRBACProvider.
func NewPAMRBACProvider(useSudo bool) *PAMRBACProvider {
	host := os.Getenv("QUADLET_HOST")
	if host == "" {
		host = "localhost"
	}
	return &PAMRBACProvider{
		pamIssuerContainer: "flightctl-pam-issuer",
		host:               host,
		sshUser:            os.Getenv("QUADLET_SSH_USER"),
		sshKeyPath:         os.Getenv("QUADLET_SSH_KEY"),
		useSudo:            useSudo,
	}
}

// isRemote returns true if the Quadlet host is remote (requires SSH).
func (p *PAMRBACProvider) isRemote() bool {
	return p.sshUser != "" && p.host != "localhost" && p.host != "127.0.0.1"
}

// runCommand executes a command, using SSH if the host is remote.
func (p *PAMRBACProvider) runCommand(command ...string) (string, error) {
	var cmd *exec.Cmd

	if p.isRemote() {
		// Build SSH command
		sshArgs := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
		if p.sshKeyPath != "" {
			sshArgs = append(sshArgs, "-i", p.sshKeyPath)
		}
		sshTarget := fmt.Sprintf("%s@%s", p.sshUser, p.host)
		sshArgs = append(sshArgs, sshTarget)

		// Build remote command with optional sudo
		remoteCmd := strings.Join(command, " ")
		if p.useSudo {
			remoteCmd = "sudo " + remoteCmd
		}
		sshArgs = append(sshArgs, remoteCmd)

		cmd = exec.Command("ssh", sshArgs...)
	} else {
		// Local execution
		if p.useSudo {
			cmd = exec.Command("sudo", command...)
		} else {
			cmd = exec.Command(command[0], command[1:]...) //nolint:gosec // G204: command args are from internal test config
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// runPodmanExec executes a command in the PAM issuer container.
func (p *PAMRBACProvider) runPodmanExec(args ...string) (string, error) {
	cmdArgs := append([]string{"podman", "exec", p.pamIssuerContainer}, args...)
	return p.runCommand(cmdArgs...)
}

// buildGroupName constructs the group name from namespace and role name.
// If namespace is provided: "<namespace>.<name>" (org-scoped)
// If namespace is empty: "<name>" (cluster-scoped)
func buildGroupName(namespace, name string) string {
	if namespace != "" {
		return namespace + "." + name
	}
	return name
}

// CreateRole creates a Linux group for the role.
// Namespaced roles become groups named "<namespace>.<name>".
func (p *PAMRBACProvider) CreateRole(_ context.Context, spec *infra.RoleSpec) error {
	if spec == nil {
		return fmt.Errorf("spec cannot be nil")
	}

	groupName := buildGroupName(spec.Namespace, spec.Name)
	return p.createGroup(groupName)
}

// UpdateRole is a no-op for PAM (groups don't have permissions).
func (p *PAMRBACProvider) UpdateRole(_ context.Context, _ *infra.RoleSpec) error {
	// No-op: Linux groups don't have permissions, they're just membership containers
	return nil
}

// DeleteRole deletes the Linux group for the role.
func (p *PAMRBACProvider) DeleteRole(_ context.Context, namespace, name string) error {
	groupName := buildGroupName(namespace, name)
	return p.deleteGroup(groupName)
}

// CreateRoleBinding adds the user to the role's group.
// For namespaced roles (namespace != "*"), also adds the user to the org-<namespace> group.
func (p *PAMRBACProvider) CreateRoleBinding(_ context.Context, spec *infra.RoleBindingSpec) error {
	if spec == nil {
		return fmt.Errorf("spec cannot be nil")
	}

	// For namespaced roles, ensure user is part of the organization group
	if spec.Namespace != "" && spec.Namespace != "*" {
		orgGroup := "org-" + spec.Namespace
		logrus.Infof("PAM RBAC: adding user %s to organization group %s", spec.Subject, orgGroup)
		if err := p.addUserToGroup(spec.Subject, orgGroup); err != nil {
			return fmt.Errorf("failed to add user to org group: %w", err)
		}
	}

	groupName := buildGroupName(spec.Namespace, spec.RoleName)
	return p.addUserToGroup(spec.Subject, groupName)
}

// DeleteRoleBinding removes the user from the role's group.
func (p *PAMRBACProvider) DeleteRoleBinding(_ context.Context, namespace, name string) error {
	// For PAM, we need the role name and user to remove.
	// Since we only have the binding name, we use it as the group name.
	// In practice, tests should track which user was bound.
	logrus.Warnf("PAM RBAC: DeleteRoleBinding called with namespace=%s, name=%s - cannot determine user to remove", namespace, name)
	return nil
}

// CreateClusterRole creates a Linux group for the cluster role (no namespace prefix).
func (p *PAMRBACProvider) CreateClusterRole(_ context.Context, spec *infra.RoleSpec) error {
	if spec == nil {
		return fmt.Errorf("spec cannot be nil")
	}

	// Cluster roles have no namespace prefix
	return p.createGroup(spec.Name)
}

// UpdateClusterRole is a no-op for PAM (groups don't have permissions).
func (p *PAMRBACProvider) UpdateClusterRole(_ context.Context, _ *infra.RoleSpec) error {
	// No-op: Linux groups don't have permissions
	return nil
}

// DeleteClusterRole deletes the Linux group for the cluster role.
func (p *PAMRBACProvider) DeleteClusterRole(_ context.Context, name string) error {
	return p.deleteGroup(name)
}

// CreateClusterRoleBinding adds the user to the cluster role's group.
func (p *PAMRBACProvider) CreateClusterRoleBinding(_ context.Context, spec *infra.RoleBindingSpec) error {
	if spec == nil {
		return fmt.Errorf("spec cannot be nil")
	}

	// Cluster role bindings use the role name directly (no namespace)
	return p.addUserToGroup(spec.Subject, spec.RoleName)
}

// DeleteClusterRoleBinding removes the user from the cluster role's group.
func (p *PAMRBACProvider) DeleteClusterRoleBinding(_ context.Context, name string) error {
	// For PAM, we need the role name and user to remove.
	// Since we only have the binding name, we cannot determine the user.
	logrus.Warnf("PAM RBAC: DeleteClusterRoleBinding called with name=%s - cannot determine user to remove", name)
	return nil
}

// CreateNamespace creates an organization by creating a Linux group named "org-<name>".
// In PAM-based RBAC, organizations are represented as Linux groups with the "org-" prefix.
func (p *PAMRBACProvider) CreateNamespace(_ context.Context, name string, _ map[string]string) error {
	groupName := "org-" + name
	logrus.Infof("PAM RBAC: creating organization group %s", groupName)
	return p.createGroup(groupName)
}

// DeleteNamespace deletes an organization by deleting the Linux group named "org-<name>".
func (p *PAMRBACProvider) DeleteNamespace(_ context.Context, name string) error {
	groupName := "org-" + name
	logrus.Infof("PAM RBAC: deleting organization group %s", groupName)
	return p.deleteGroup(groupName)
}

// --- Internal helper methods ---

// createGroup creates a Linux group in the PAM issuer container.
func (p *PAMRBACProvider) createGroup(groupName string) error {
	_, err := p.runPodmanExec("groupadd", groupName)
	if err != nil {
		// Ignore "already exists" errors
		if strings.Contains(err.Error(), "already exists") {
			logrus.Debugf("PAM RBAC: group %s already exists", groupName)
			return nil
		}
		return fmt.Errorf("failed to create group %s: %w", groupName, err)
	}
	logrus.Infof("PAM RBAC: created group %s", groupName)
	return nil
}

// deleteGroup deletes a Linux group from the PAM issuer container.
func (p *PAMRBACProvider) deleteGroup(groupName string) error {
	_, err := p.runPodmanExec("groupdel", groupName)
	if err != nil {
		// Ignore "does not exist" errors
		if strings.Contains(err.Error(), "does not exist") {
			logrus.Debugf("PAM RBAC: group %s does not exist", groupName)
			return nil
		}
		return fmt.Errorf("failed to delete group %s: %w", groupName, err)
	}
	logrus.Infof("PAM RBAC: deleted group %s", groupName)
	return nil
}

// addUserToGroup adds a user to a group.
func (p *PAMRBACProvider) addUserToGroup(username, groupName string) error {
	_, err := p.runPodmanExec("usermod", "-aG", groupName, username)
	if err != nil {
		return fmt.Errorf("failed to add user %s to group %s: %w", username, groupName, err)
	}
	logrus.Infof("PAM RBAC: added user %s to group %s", username, groupName)
	return nil
}

// removeUserFromGroup removes a user from a group.
func (p *PAMRBACProvider) removeUserFromGroup(username, groupName string) error {
	_, err := p.runPodmanExec("gpasswd", "-d", username, groupName)
	if err != nil {
		return fmt.Errorf("failed to remove user %s from group %s: %w", username, groupName, err)
	}
	logrus.Infof("PAM RBAC: removed user %s from group %s", username, groupName)
	return nil
}

// --- PAM-specific convenience methods for tests ---

// CreateUser creates a user in the PAM issuer container.
func (p *PAMRBACProvider) CreateUser(username string) error {
	_, err := p.runPodmanExec("adduser", username)
	if err != nil {
		return fmt.Errorf("failed to create user %s: %w", username, err)
	}
	logrus.Infof("PAM RBAC: created user %s", username)
	return nil
}

// DeleteUser deletes a user from the PAM issuer container.
func (p *PAMRBACProvider) DeleteUser(username string) error {
	_, err := p.runPodmanExec("userdel", "-r", username)
	if err != nil {
		return fmt.Errorf("failed to delete user %s: %w", username, err)
	}
	logrus.Infof("PAM RBAC: deleted user %s", username)
	return nil
}

// SetUserPassword sets the password for a user in the PAM issuer container.
func (p *PAMRBACProvider) SetUserPassword(username, password string) error {
	// Use chpasswd via shell to set password
	cmd := fmt.Sprintf("echo '%s:%s' | chpasswd", username, password)
	_, err := p.runPodmanExec("sh", "-c", cmd)
	if err != nil {
		return fmt.Errorf("failed to set password for user %s: %w", username, err)
	}
	logrus.Infof("PAM RBAC: set password for user %s", username)
	return nil
}

// GetUserGroups returns the groups a user belongs to.
func (p *PAMRBACProvider) GetUserGroups(username string) ([]string, error) {
	output, err := p.runPodmanExec("groups", username)
	if err != nil {
		return nil, fmt.Errorf("failed to get groups for user %s: %w", username, err)
	}
	// Output format: "username : group1 group2 group3"
	parts := strings.SplitN(output, ":", 2)
	if len(parts) < 2 {
		return nil, nil
	}
	groups := strings.Fields(strings.TrimSpace(parts[1]))
	return groups, nil
}

// --- PAM Role Constants ---
// These constants define the standard FlightCtl role group names.

const (
	// RoleAdmin is the global administrator role (full access everywhere).
	RoleAdmin = "flightctl-admin"
	// RoleOrgAdmin is the organization administrator role.
	RoleOrgAdmin = "flightctl-org-admin"
	// RoleOperator can manage devices, fleets, and imagebuilds.
	RoleOperator = "flightctl-operator"
	// RoleViewer has read-only access.
	RoleViewer = "flightctl-viewer"
	// RoleInstaller can provision devices and download imageexports.
	RoleInstaller = "flightctl-installer"
)

// OrgGroupName returns the group name for an organization (e.g., "org-engineering").
func OrgGroupName(organization string) string {
	return "org-" + organization
}

// SetupTestUser creates a test user with the specified role and optional organization.
// This is a convenience method for test setup.
func (p *PAMRBACProvider) SetupTestUser(username, password, role string, organization string) error {
	// Create role group if needed
	if err := p.createGroup(role); err != nil {
		return err
	}

	// Create organization group if specified
	if organization != "" {
		if err := p.createGroup(OrgGroupName(organization)); err != nil {
			return err
		}
	}

	// Create user
	if err := p.CreateUser(username); err != nil {
		return err
	}

	// Set password
	if err := p.SetUserPassword(username, password); err != nil {
		return err
	}

	// Add to role group
	if err := p.addUserToGroup(username, role); err != nil {
		return err
	}

	// Add to organization if specified
	if organization != "" {
		if err := p.addUserToGroup(username, OrgGroupName(organization)); err != nil {
			return err
		}
	}

	return nil
}

// CleanupTestUser removes a test user and associated resources.
func (p *PAMRBACProvider) CleanupTestUser(username string) error {
	return p.DeleteUser(username)
}

// BindUserToRole is a convenience method to bind a user to a role.
// For namespaced roles, provide namespace. For cluster roles, leave namespace empty.
func (p *PAMRBACProvider) BindUserToRole(username, namespace, roleName string) error {
	groupName := buildGroupName(namespace, roleName)
	return p.addUserToGroup(username, groupName)
}

// UnbindUserFromRole is a convenience method to unbind a user from a role.
func (p *PAMRBACProvider) UnbindUserFromRole(username, namespace, roleName string) error {
	groupName := buildGroupName(namespace, roleName)
	return p.removeUserFromGroup(username, groupName)
}
