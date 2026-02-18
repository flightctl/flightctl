package standalone

import (
	"fmt"
	"os/exec"
	"strings"
)

// GetHostnameFQDN returns the fully qualified domain name of the host.
// It first tries "hostname -f" for the FQDN, then falls back to "hostname"
// for the short name. The result is normalized to lowercase per RFC 1123
// (DNS is case-insensitive).
func GetHostnameFQDN() (string, error) {
	// Try "hostname -f" first for FQDN
	cmd := exec.Command("hostname", "-f")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		hostname := strings.TrimSpace(string(output))
		if hostname != "" {
			return strings.ToLower(hostname), nil
		}
	}

	// Fallback to "hostname" (short name)
	cmd = exec.Command("hostname")
	output, err = cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute hostname command: %w", err)
	}

	hostname := strings.TrimSpace(string(output))
	if hostname == "" {
		return "", fmt.Errorf("hostname command returned empty value")
	}

	return strings.ToLower(hostname), nil
}
