package packaging_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	selinuxPolicyPath               = "packaging/selinux/flightctl_agent.te"
	systemdStopAllServicesMacro     = "systemd_stop_all_services(flightctl_agent_t)"
	systemdStopSystemdServicesMacro = "systemd_stop_systemd_services(flightctl_agent_t)"
)

// TestSELinuxPolicyAllowsStoppingAllSystemdServices verifies that the agent can stop Quadlet-generated services.
func TestSELinuxPolicyAllowsStoppingAllSystemdServices(t *testing.T) {
	root := repoRoot(t)

	policyPath := filepath.Join(root, selinuxPolicyPath)
	policy, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read SELinux policy: %v", err)
	}

	policyText := activePolicyText(policy)
	if !strings.Contains(policyText, systemdStopAllServicesMacro) {
		t.Fatalf("SELinux policy does not allow stopping all systemd services")
	}
	if strings.Contains(policyText, systemdStopSystemdServicesMacro) {
		t.Fatalf("SELinux policy still uses the narrow systemd stop macro")
	}
}

// activePolicyText returns policy text with full-line comments removed.
func activePolicyText(policy []byte) string {
	activeLines := make([]string, 0)
	for _, line := range strings.Split(string(policy), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		activeLines = append(activeLines, line)
	}
	return strings.Join(activeLines, "\n")
}
