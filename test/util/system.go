package util

import (
	"fmt"
	"os/exec"
	"strings"
)

// IsPortListening checks if a port is listening
func IsPortListening(port string) (bool, error) {
	cmd := exec.Command("sudo", "ss", "-tlnp")
	output, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check listening ports: %w", err)
	}

	return strings.Contains(string(output), ":"+port), nil
}

// GetOSRelease gets OS release information from /etc/os-release
func GetOSRelease() (string, error) {
	cmd := exec.Command("cat", "/etc/os-release")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to read /etc/os-release: %w", err)
	}

	return string(output), nil
}

// GetKernelInfo gets kernel information
func GetKernelInfo() (string, error) {
	cmd := exec.Command("uname", "-r")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get kernel information: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// GetSELinuxStatus gets SELinux status
func GetSELinuxStatus() (string, error) {
	cmd := exec.Command("getenforce")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to get SELinux status: %w", err)
	}

	return strings.TrimSpace(string(output)), nil
}

