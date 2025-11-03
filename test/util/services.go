package util

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

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
	cmd := exec.Command("systemctl", "list-unit-files", unitName)
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to list systemd units: %w", err)
	}

	return strings.Contains(string(output), unitName), nil
}

// ListSystemdUnits lists all systemd units matching a pattern
func ListSystemdUnits(pattern string) ([]string, error) {
	cmd := exec.Command("sudo", "systemctl", "list-units", "--no-legend", pattern)
	output, err := cmd.Output()
	if err != nil {
		// If no units found, systemctl might return error
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 0 {
			return []string{}, nil
		}
		return nil, fmt.Errorf("failed to list systemd units: %w", err)
	}

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
		cmd := exec.Command("sudo", "systemctl", "is-active", serviceName)
		output, err := cmd.Output()
		if err == nil && strings.TrimSpace(string(output)) == "active" {
			return nil
		}

		time.Sleep(2 * time.Second)
	}

	return fmt.Errorf("timeout waiting for service %s to be ready", serviceName)
}

