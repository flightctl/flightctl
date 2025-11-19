package e2e

import (
	"fmt"
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
