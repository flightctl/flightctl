package e2e

import (
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/util"
)

// GetAgentConfigDir returns the prepared e2e agent config directory created by make prepare-e2e-test.
func GetAgentConfigDir() string {
	return filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
}

// GetAgentConfigPath returns the prepared agent config.yaml path for e2e tests.
func GetAgentConfigPath(agentConfigDir string) string {
	if agentConfigDir == "" {
		agentConfigDir = GetAgentConfigDir()
	}
	return filepath.Join(agentConfigDir, "config.yaml")
}

// GetAgentCertsDir returns the prepared agent certs directory path for e2e tests.
func GetAgentCertsDir(agentConfigDir string) string {
	if agentConfigDir == "" {
		agentConfigDir = GetAgentConfigDir()
	}
	return filepath.Join(agentConfigDir, "certs")
}

// AgentConfigDirExists reports whether the prepared agent config and certs are present for package-mode tests.
func AgentConfigDirExists() bool {
	configDir := GetAgentConfigDir()
	configPath := GetAgentConfigPath(configDir)
	certsDir := GetAgentCertsDir(configDir)

	if _, err := os.Stat(configPath); err != nil {
		return false
	}
	if _, err := os.Stat(certsDir); err != nil {
		return false
	}
	return true
}
