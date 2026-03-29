package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flightctl/flightctl/test/util"
)

var validUnixAccountName = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)

// IsAuthDisabled checks whether the environment has auth disabled for flightctl login.
func (h *Harness) IsAuthDisabled(apiEndpoint, configDir string) bool {
	if h == nil || strings.TrimSpace(apiEndpoint) == "" {
		return false
	}
	out, _, _ := h.CLIWithConfigAndStdinExitCode(
		configDir,
		"",
		"login",
		apiEndpoint,
		"--token",
		"fake-token",
		"--insecure-skip-tls-verify",
	)
	return strings.Contains(strings.ToLower(strings.TrimSpace(out)), "auth is disabled")
}

// IsPodmanContainerRunning returns true when the named podman container is running.
func (h *Harness) IsPodmanContainerRunning(container string) bool {
	if h == nil || strings.TrimSpace(container) == "" || !util.BinaryExistsOnPath("podman") {
		return false
	}
	out, err := h.SH("podman", "ps", "--format", "{{.Names}}")
	return err == nil && strings.Contains(out, container)
}

// IsQuadletsEnvironment reports whether FlightCtl appears to be deployed via quadlets.
// Detection order:
// 1. Explicit env override (FLIGHTCTL_QUADLETS/QUADLETS)
// 2. Active systemd target (flightctl.target)
// 3. Presence of rendered quadlet files (flightctl*.container)
func (h *Harness) IsQuadletsEnvironment() bool {
	if util.IsTruthy(util.EnvFirst("FLIGHTCTL_QUADLETS", "QUADLETS")) {
		return true
	}

	if h != nil && util.BinaryExistsOnPath("systemctl") {
		if _, err := h.SH("systemctl", "is-active", "--quiet", "flightctl.target"); err == nil {
			return true
		}
	}

	quadletDir := util.DefaultIfEmpty(util.EnvFirst("QUADLET_FILES_OUTPUT_DIR"), "/usr/share/containers/systemd")
	if info, err := os.Stat(quadletDir); err == nil && info.IsDir() {
		matches, globErr := filepath.Glob(filepath.Join(quadletDir, "flightctl*.container"))
		if globErr == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

// ProvisionPAMUser creates or updates a PAM user, password, and group memberships in the specified container.
func (h *Harness) ProvisionPAMUser(container, user, pass string, groups []string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("container is empty")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("pam user cannot be empty")
	}
	if strings.TrimSpace(pass) == "" {
		return fmt.Errorf("pam password cannot be empty")
	}
	if !validUnixAccountName.MatchString(user) {
		return fmt.Errorf("pam user %q has invalid format", user)
	}
	if strings.ContainsAny(pass, "\r\n") {
		return fmt.Errorf("pam password contains newline characters")
	}
	for _, g := range groups {
		if !validUnixAccountName.MatchString(g) {
			return fmt.Errorf("pam group %q has invalid format", g)
		}
	}
	if len(groups) == 0 {
		if _, err := h.SH("podman", "exec", "-i", container, "id", "-u", user); err != nil {
			if _, createErr := h.SH("podman", "exec", "-i", container, "useradd", "-m", user); createErr != nil {
				return fmt.Errorf("failed creating PAM user %q: %w", user, createErr)
			}
		}
		if _, err := h.SHWithStdin(fmt.Sprintf("%s:%s\n", user, pass), "podman", "exec", "-i", container, "chpasswd"); err != nil {
			return fmt.Errorf("failed setting PAM user password: %w", err)
		}
		return nil
	}

	for _, g := range groups {
		if _, err := h.SH("podman", "exec", "-i", container, "getent", "group", g); err != nil {
			if _, createErr := h.SH("podman", "exec", "-i", container, "groupadd", g); createErr != nil {
				return fmt.Errorf("failed creating PAM group %q: %w", g, createErr)
			}
		}
	}

	if _, err := h.SH("podman", "exec", "-i", container, "id", "-u", user); err != nil {
		if _, createErr := h.SH("podman", "exec", "-i", container, "useradd", "-m", user); createErr != nil {
			return fmt.Errorf("failed creating PAM user %q: %w", user, createErr)
		}
	}

	if _, err := h.SHWithStdin(fmt.Sprintf("%s:%s\n", user, pass), "podman", "exec", "-i", container, "chpasswd"); err != nil {
		return fmt.Errorf("failed setting PAM user password: %w", err)
	}

	for _, g := range groups {
		if _, err := h.SH("podman", "exec", "-i", container, "usermod", "-aG", g, user); err != nil {
			return fmt.Errorf("failed assigning PAM group %q: %w", g, err)
		}
	}

	groupsOut, err := h.SH("podman", "exec", container, "groups", user)
	if err != nil {
		return fmt.Errorf("failed reading PAM user groups: %w", err)
	}
	trimmedGroupsOut := strings.TrimSpace(groupsOut)
	if trimmedGroupsOut == "" {
		return fmt.Errorf("failed reading PAM user groups: empty output")
	}
	groupTokens := strings.Fields(trimmedGroupsOut)
	for _, g := range groups {
		matched := false
		for _, token := range groupTokens {
			if token == g {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("PAM user %q missing group %q", user, g)
		}
	}
	return nil
}

// DeletePAMUser removes a PAM user from the specified container if the user exists.
func (h *Harness) DeletePAMUser(container, user string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(container) == "" {
		return fmt.Errorf("container is empty")
	}
	if strings.TrimSpace(user) == "" {
		return fmt.Errorf("pam user cannot be empty")
	}
	if !validUnixAccountName.MatchString(user) {
		return fmt.Errorf("pam user %q has invalid format", user)
	}
	if _, err := h.SH("podman", "exec", "-i", container, "id", "-u", user); err != nil {
		return nil
	}
	if _, err := h.SH("podman", "exec", "-i", container, "userdel", "-r", user); err != nil {
		return fmt.Errorf("failed deleting PAM user %q: %w", user, err)
	}
	return nil
}

// CleanupNamespace removes a namespace via client-go when available, falling back to kubectl.
func (h *Harness) CleanupNamespace(namespace string) {
	if h == nil || strings.TrimSpace(namespace) == "" {
		return
	}
	_, _ = h.SH("kubectl", "delete", "namespace", namespace, "--wait=false")
}

// EnsureNamespace ensures a namespace exists.
func (h *Harness) EnsureNamespace(namespace string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("namespace is empty")
	}
	_, err := h.SH("kubectl", "create", "namespace", namespace)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("create namespace %q: %w", namespace, err)
	}
	return nil
}

// EnsureServiceAccount ensures a service account exists in a namespace.
func (h *Harness) EnsureServiceAccount(namespace, serviceAccount string) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(namespace) == "" {
		return fmt.Errorf("namespace is empty")
	}
	if strings.TrimSpace(serviceAccount) == "" {
		return fmt.Errorf("service account is empty")
	}
	_, err := h.SH("kubectl", "-n", namespace, "create", "sa", serviceAccount)
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("create service account %q in namespace %q: %w", serviceAccount, namespace, err)
	}
	return nil
}
