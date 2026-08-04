package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
)

// PrepareAgentConfigForContainer runs the prepare_agent_config.sh script to generate
// agent config and certs for use with testcontainer-based e2e tests.
func PrepareAgentConfigForContainer(ctx context.Context) error {
	topLevel := util.GetTopLevelDir()
	scriptPath := filepath.Join(topLevel, "test", "scripts", "agent-images", "prepare_agent_config.sh")

	if _, err := os.Stat(scriptPath); err != nil {
		return fmt.Errorf("missing prepare_agent_config.sh at %s: %w", scriptPath, err)
	}

	cmd := exec.CommandContext(ctx, scriptPath)
	cmd.Dir = topLevel
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("running prepare_agent_config.sh: %w\noutput: %s", err, string(output))
	}

	logrus.Info("Agent config prepared for container-based tests")
	return nil
}

// GetAgentConfigDir returns the path to the agent config directory (bin/agent/etc/flightctl).
// The directory contains config.yaml and certs/ subdirectory.
func GetAgentConfigDir() string {
	return filepath.Join(util.GetTopLevelDir(), "bin", "agent", "etc", "flightctl")
}

// AgentConfigDirExists returns true if the agent config directory exists with the required files.
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
