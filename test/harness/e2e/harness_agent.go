package e2e

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// StartFlightCtlAgent starts the flightctl-agent service
func (h *Harness) StartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "start", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to start flightctl-agent: %w", err)
	}
	return nil
}

// StopFlightCtlAgent stops the flightctl-agent service
func (h *Harness) StopFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to stop flightctl-agent: %w", err)
	}
	return nil
}

// RestartFlightCtlAgent restarts the flightctl-agent service
func (h *Harness) RestartFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to restart flightctl-agent: %w", err)
	}
	return nil
}

// ReloadFlightCtlAgent reloads the flightctl-agent service
func (h *Harness) ReloadFlightCtlAgent() error {
	_, err := h.VM.RunSSH([]string{"sudo", "systemctl", "reload", "flightctl-agent"}, nil)
	if err != nil {
		return fmt.Errorf("failed to reload flightctl-agent: %w", err)
	}
	return nil
}

// InstallFlightCtlAgentFromCopr installs FlightCtl agent from Copr repository
// coprRepo: Copr repository identifier (e.g., "@redhat-et/flightctl-dev")
// Returns error if installation fails
func InstallFlightCtlAgentFromCopr(coprRepo string) error {
	// Check if agent is already installed
	if _, err := exec.LookPath("flightctl-agent"); err == nil {
		// Agent already installed, check for updates
		if err := runSystemCommand("dnf", "upgrade", "-y", "flightctl"); err != nil {
			// Upgrade failed, but agent exists - not a critical error
			// Continue to verify installation
		}
	} else {
		// Enable Copr repository
		if err := runSystemCommand("dnf", "copr", "enable", "-y", coprRepo); err != nil {
			return fmt.Errorf("failed to enable Copr repository %s: %w", coprRepo, err)
		}

		// Install flightctl package
		if err := runSystemCommand("dnf", "install", "-y", "flightctl"); err != nil {
			return fmt.Errorf("failed to install flightctl package: %w", err)
		}
	}

	// Verify agent binary exists
	_, err := exec.LookPath("flightctl-agent")
	if err != nil {
		return fmt.Errorf("flightctl-agent binary not found after installation: %w", err)
	}

	// Get agent version
	_, err = runSystemCommandWithOutput("flightctl-agent", "--version")
	if err != nil {
		// Version check failed but binary exists - not critical
		return nil
	}

	// Successfully verified installation
	return nil
}

// ConfigureAgentWithTPM creates FlightCtl agent configuration with TPM enabled
// apiURL: FlightCtl API server URL
// tpmDevice: TPM device path (usually /dev/tpm0)
// configPath: Path to write configuration file
// dataDir: Agent data directory
// Returns error if configuration fails
func ConfigureAgentWithTPM(apiURL, tpmDevice, configPath, dataDir string) error {
	// Create agent configuration with TPM enabled
	agentConfig := fmt.Sprintf(`server:
  url: %s
  
tpm:
  enable: true
  device: %s

enrollment:
  approve: false

log:
  level: debug
`, apiURL, tpmDevice)

	// Write configuration using echo and sudo
	cmd := exec.Command("bash", "-c", fmt.Sprintf("echo '%s' | sudo tee %s > /dev/null", agentConfig, configPath))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to write agent configuration to %s: %w", configPath, err)
	}

	// Ensure data directory exists
	if err := runSystemCommand("mkdir", "-p", dataDir); err != nil {
		return fmt.Errorf("failed to create data directory %s: %w", dataDir, err)
	}

	return nil
}

// StartAgentServiceAndWaitForTPM starts the flightctl-agent service and waits for TPM initialization
// serviceName: Name of the systemd service
// Returns error if service fails to start or TPM initialization fails
func StartAgentServiceAndWaitForTPM(serviceName string) error {
	// Enable service
	if err := runSystemCommand("systemctl", "enable", serviceName); err != nil {
		return fmt.Errorf("failed to enable service %s: %w", serviceName, err)
	}

	// Start service
	if err := runSystemCommand("systemctl", "start", serviceName); err != nil {
		return fmt.Errorf("failed to start service %s: %w", serviceName, err)
	}

	// Wait for service to be active (max 30 seconds)
	for i := 0; i < 15; i++ {
		output, err := runSystemCommandWithOutput("systemctl", "is-active", serviceName)
		if err == nil && strings.Contains(output, "active") {
			break
		}
		if i == 14 {
			return fmt.Errorf("service %s did not become active within 30 seconds", serviceName)
		}
		time.Sleep(2 * time.Second)
	}

	// Wait for TPM initialization in logs (max 30 seconds)
	for i := 0; i < 6; i++ {
		logs, err := runSystemCommandWithOutput("journalctl", "-u", serviceName, "--since", "1 minute ago", "--no-pager")
		if err == nil && strings.Contains(logs, "Using TPM-based identity provider") {
			return nil
		}
		if i == 5 {
			return fmt.Errorf("TPM initialization not detected in service logs within 30 seconds")
		}
		time.Sleep(5 * time.Second)
	}

	return nil
}

// Helper functions for running system commands

func runSystemCommand(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("command failed: %s %v\nOutput: %s\nError: %w", name, args, string(output), err)
	}
	return nil
}

func runSystemCommandWithOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("command failed: %s %v\nOutput: %s\nError: %w", name, args, string(output), err)
	}
	return string(output), nil
}
