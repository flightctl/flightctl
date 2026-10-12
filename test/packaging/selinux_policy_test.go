package packaging_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSELinuxPolicyAllowsStoppingAllSystemdServices(t *testing.T) {
	root := repoRoot(t)

	policyPath := filepath.Join(root, "packaging/selinux/flightctl_agent.te")
	policy, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatalf("read SELinux policy: %v", err)
	}

	policyText := string(policy)
	if !strings.Contains(policyText, "systemd_stop_all_services(flightctl_agent_t)") {
		t.Fatalf("SELinux policy does not allow stopping all systemd services")
	}
	if strings.Contains(policyText, "systemd_stop_systemd_services(flightctl_agent_t)") {
		t.Fatalf("SELinux policy still uses the narrow systemd stop macro")
	}
}
