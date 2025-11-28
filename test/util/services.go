package util

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// runSystemctl executes a systemctl command with optional sudo
// useSudo determines whether to prefix the command with "sudo"
// command is the systemctl subcommand (e.g., "is-active", "list-units")
// args are additional arguments to pass to systemctl
func runSystemctl(useSudo bool, command string, args ...string) ([]byte, error) {
	var cmd *exec.Cmd
	if useSudo {
		//nolint:gosec // G204: args are controlled by internal code, not user input
		cmd = exec.Command("sudo", append([]string{"systemctl", command}, args...)...)
	} else {
		//nolint:gosec // G204: args are controlled by internal code, not user input
		cmd = exec.Command("systemctl", append([]string{command}, args...)...)
	}
	return cmd.Output()
}

// IsRPMInstalled checks if an RPM package is installed and returns its version
func IsRPMInstalled(packageName string) (bool, string, error) {
	cmd := exec.Command("rpm", "-q", packageName)
	output, err := cmd.Output()
	if err != nil {
		// rpm -q returns error if package is not installed
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, "", nil
		}
		return false, "", fmt.Errorf("failed to query RPM: %w", err)
	}

	version := strings.TrimSpace(string(output))
	return true, version, nil
}

// SystemdUnitExists checks if a systemd unit exists
func SystemdUnitExists(unitName string) (bool, error) {
	output, err := runSystemctl(false, "list-unit-files", unitName)
	if err != nil {
		return false, fmt.Errorf("failed to list systemd units: %w", err)
	}

	return strings.Contains(string(output), unitName), nil
}

// ListSystemdUnits lists all systemd units matching a pattern
func ListSystemdUnits(pattern string) ([]string, error) {
	output, err := runSystemctl(true, "list-units", "--no-legend", pattern)
	if err != nil {
		// systemctl list-units returns exit code 0 when patterns match nothing (no error)
		// Non-zero exit codes indicate real errors (permission denied, systemd unavailable, etc.)
		// We should propagate these errors rather than masking them
		return nil, fmt.Errorf("failed to list systemd units: %w", err)
	}

	// Exit code 0 means success (even if no units matched)
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	units := []string{}
	for _, line := range lines {
		if line != "" {
			parts := strings.Fields(line)
			if len(parts) > 0 {
				units = append(units, parts[0])
			}
		}
	}

	return units, nil
}

// GetSystemdStatus gets the status of systemd units matching a pattern
func GetSystemdStatus(pattern string) (string, error) {
	// Use CombinedOutput to capture both stdout and stderr
	cmd := exec.Command("sudo", "systemctl", "status", pattern)
	output, _ := cmd.CombinedOutput()
	// systemctl status returns non-zero if any service is not active, but we still want the output
	return string(output), nil
}

// GetJournalLogs retrieves systemd journal logs for a service
func GetJournalLogs(serviceName string, since string) (string, error) {
	cmd := exec.Command("sudo", "journalctl", "-u", serviceName, "--since", since, "--no-pager")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get journal logs: %w", err)
	}

	return string(output), nil
}

// WaitForServiceReady waits for a service to be active
func WaitForServiceReady(serviceName string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		output, err := runSystemctl(true, "is-active", serviceName)
		if err == nil && strings.EqualFold(strings.TrimSpace(string(output)), "active") {
			return nil
		}

		time.Sleep(POLLING)
	}

	return fmt.Errorf("timeout waiting for service %s to be ready", serviceName)
}
