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
	systemdStopCompatibilityMacro   = "ifdef(`systemd_stop_all_services',"
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
	if !strings.Contains(policyText, systemdStopCompatibilityMacro) {
		t.Fatalf("SELinux policy does not provide a compatible stop macro")
	}
	if !strings.Contains(policyText, systemdStopAllServicesMacro) || !strings.Contains(policyText, systemdStopSystemdServicesMacro) {
		t.Fatalf("SELinux policy does not provide both systemd stop macro paths")
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
