// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// ServiceLifecycleProvider implements infra.ServiceLifecycleProvider for Quadlet environments.
type ServiceLifecycleProvider struct {
	// useSudo indicates whether to use sudo for systemctl commands
	useSudo bool
}

// NewServiceLifecycleProvider creates a new Quadlet ServiceLifecycleProvider.
func NewServiceLifecycleProvider(useSudo bool) *ServiceLifecycleProvider {
	return &ServiceLifecycleProvider{
		useSudo: useSudo,
	}
}

// serviceToSystemdUnit returns the systemd unit name for a service.
func (p *ServiceLifecycleProvider) serviceToSystemdUnit(service infra.ServiceName) string {
	return GetServiceInfo(service).SystemdUnit
}

// runSystemctl executes a systemctl command.
func (p *ServiceLifecycleProvider) runSystemctl(args ...string) (string, error) {
	var cmd *exec.Cmd
	if p.useSudo {
		cmd = exec.Command("sudo", append([]string{"systemctl"}, args...)...) //nolint:gosec // G204: args are from internal test config
	} else {
		cmd = exec.Command("systemctl", args...)
	}

	output, err := cmd.CombinedOutput()
	return string(output), err
}

// IsRunning checks if a service is currently running.
func (p *ServiceLifecycleProvider) IsRunning(service infra.ServiceName) (bool, error) {
	unit := p.serviceToSystemdUnit(service)

	output, err := p.runSystemctl("is-active", unit)
	if err != nil {
		// is-active returns non-zero for inactive services
		return false, nil
	}

	status := strings.TrimSpace(output)
	return strings.EqualFold(status, "active"), nil
}

// Start starts a stopped service.
func (p *ServiceLifecycleProvider) Start(service infra.ServiceName) error {
	unit := p.serviceToSystemdUnit(service)

	_, err := p.runSystemctl("start", unit)
	if err != nil {
		return fmt.Errorf("failed to start %s: %w", unit, err)
	}

	logrus.Infof("Quadlet: started service %s", unit)
	return nil
}

// Stop stops a running service.
func (p *ServiceLifecycleProvider) Stop(service infra.ServiceName) error {
	unit := p.serviceToSystemdUnit(service)

	_, err := p.runSystemctl("stop", unit)
	if err != nil {
		return fmt.Errorf("failed to stop %s: %w", unit, err)
	}

	logrus.Infof("Quadlet: stopped service %s", unit)
	return nil
}

// Restart restarts a service.
func (p *ServiceLifecycleProvider) Restart(service infra.ServiceName) error {
	unit := p.serviceToSystemdUnit(service)

	_, err := p.runSystemctl("restart", unit)
	if err != nil {
		return fmt.Errorf("failed to restart %s: %w", unit, err)
	}

	logrus.Infof("Quadlet: restarted service %s", unit)
	return nil
}

// WaitForReady waits for a service to be ready.
func (p *ServiceLifecycleProvider) WaitForReady(service infra.ServiceName, timeout time.Duration) error {
	unit := p.serviceToSystemdUnit(service)

	deadline := time.Now().Add(timeout)
	polling := 250 * time.Millisecond

	for time.Now().Before(deadline) {
		output, err := p.runSystemctl("is-active", unit)
		if err == nil && strings.EqualFold(strings.TrimSpace(output), "active") {
			logrus.Infof("Quadlet: service %s is ready", unit)
			return nil
		}
		time.Sleep(polling)
	}

	return fmt.Errorf("timeout waiting for %s to be ready", unit)
}

// AreServicesHealthy checks if all flightctl services are healthy by checking the flightctl.target.
func (p *ServiceLifecycleProvider) AreServicesHealthy() (bool, error) {
	output, err := p.runSystemctl("is-active", "flightctl.target")
	if err != nil || !strings.EqualFold(strings.TrimSpace(output), "active") {
		logrus.Warnf("Quadlet: flightctl.target is not active")
		return false, nil
	}

	logrus.Info("Quadlet: flightctl.target is healthy")
	return true, nil
}
