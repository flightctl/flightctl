package e2e

import (
	"context"
	"fmt"
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
	ctx := h.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if h.Cluster != nil {
		_ = util.DeleteNamespace(ctx, h.Cluster, namespace)
		return
	}
	_, _ = h.SH("kubectl", "delete", "namespace", namespace, "--wait=false")
}
