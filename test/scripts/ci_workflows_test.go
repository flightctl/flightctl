package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type workflowStep struct {
	Name string            `yaml:"name"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	With map[string]string `yaml:"with"`
}

func TestSmokeDeploymentPrerequisites(t *testing.T) {
	t.Run("When smoke deploys rootless Kind it should provision KVM access", func(t *testing.T) {
		data, err := os.ReadFile("../../.github/workflows/pr-smoke-testing.yaml")
		require.NoError(t, err)
		var workflow struct {
			Jobs map[string]struct {
				Steps []workflowStep `yaml:"steps"`
			} `yaml:"jobs"`
		}
		require.NoError(t, yaml.Unmarshal(data, &workflow))
		for _, step := range workflow.Jobs["smoke"].Steps {
			if step.Uses == "./.github/actions/setup-dependencies" {
				require.NotEmpty(t, step.With["setup_kvm"], "rootless deployment requires readable and writable /dev/kvm")
				return
			}
		}
		t.Fatal("smoke workflow does not provision deployment dependencies")
	})
}

func TestHelmDeploymentDNSSetup(t *testing.T) {
	data, err := os.ReadFile("../../.github/actions/deploy-backend-with-helm/action.yaml")
	require.NoError(t, err)
	var action struct {
		Runs struct {
			Steps []workflowStep `yaml:"steps"`
		} `yaml:"runs"`
	}
	require.NoError(t, yaml.Unmarshal(data, &action))
	var dnsCommand string
	for _, step := range action.Runs.Steps {
		if step.Name == "Setup local DNS (replace external nip.io)" {
			dnsCommand = step.Run
			break
		}
	}
	require.NotEmpty(t, dnsCommand)

	tests := []struct {
		name       string
		kubeconfig string
	}{
		{name: "When kubeconfig is unset it should use the runner configuration"},
		{name: "When kubeconfig is explicit it should preserve its value", kubeconfig: "/tmp/runner config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			binDir := filepath.Join(workspace, "bin")
			scriptDir := filepath.Join(workspace, "test", "scripts")
			require.NoError(t, os.MkdirAll(binDir, 0755))
			require.NoError(t, os.MkdirAll(scriptDir, 0755))
			// Model elevation without changing the test process's host privileges.
			require.NoError(t, os.WriteFile(filepath.Join(binDir, "sudo"), []byte("#!/bin/bash\nexport TEST_ROOT=1\nexec \"$@\"\n"), 0755))
			require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "setup_local_dns.sh"), []byte("#!/bin/bash\nset -eu\n[[ ${TEST_ROOT:-} == 1 ]]\nprintf '%s\\n%s\\n' \"$KUBECONFIG\" \"$1\"\n"), 0755))
			t.Setenv("KUBECONFIG", tt.kubeconfig)
			t.Setenv("BASE_DOMAIN", "192.0.2.1.nip.io")
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd := exec.Command("bash", "-euo", "pipefail", "-c", dnsCommand)
			cmd.Dir = workspace
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, string(output))
			expectedConfig := tt.kubeconfig
			if expectedConfig == "" {
				expectedConfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
			}
			require.Equal(t, expectedConfig+"\n192.0.2.1\n", string(output))
		})
	}
}
