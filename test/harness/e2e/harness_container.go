package e2e

import (
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/util"
)

func GetAgentConfigDir() string {
	return filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
}

func AgentConfigDirExists() bool {
	configDir := GetAgentConfigDir()
	configPath := filepath.Join(configDir, "config.yaml")
	certsDir := filepath.Join(configDir, "certs")

	if _, err := os.Stat(configPath); err != nil {
		return false
	}
	if _, err := os.Stat(certsDir); err != nil {
		return false
	}
	return true
}
