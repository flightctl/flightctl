// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
)

// ServiceLifecycleProvider implements infra.ServiceLifecycleProvider for Quadlet environments.
type ServiceLifecycleProvider struct {
	infra   *InfraProvider
	useSudo bool
}

// NewServiceLifecycleProvider creates a new Quadlet ServiceLifecycleProvider.
func NewServiceLifecycleProvider(infraP *InfraProvider, useSudo bool) *ServiceLifecycleProvider {
	return &ServiceLifecycleProvider{
		infra:   infraP,
		useSudo: useSudo,
	}
}

// serviceToSystemdUnit returns the systemd unit name for a service.
func (p *ServiceLifecycleProvider) serviceToSystemdUnit(service infra.ServiceName) string {
	return GetServiceInfo(service).SystemdUnit
}

// runSystemctl executes a systemctl command.
func (p *ServiceLifecycleProvider) runSystemctl(args ...string) (string, error) {
	// Delegate to the quadlet InfraProvider so this works for both local and remote quadlet hosts.
	// InfraProvider is responsible for SSH and sudo handling.
	if p.infra == nil {
		return "", fmt.Errorf("quadlet infra provider is nil")
	}
	return p.infra.RunCommand(append([]string{"systemctl"}, args...)...)
}

// isKnownInactiveOutput returns true if the trimmed systemctl is-active output
// is a normal non-active state (inactive, unknown), so the caller can treat it as (false, nil).
func isKnownInactiveOutput(trimmed string) bool {
	switch strings.ToLower(trimmed) {
	case "inactive", "unknown":
		return true
	default:
		return false
	}
}

// IsRunning checks if a service is currently running.
func (p *ServiceLifecycleProvider) IsRunning(service infra.ServiceName) (bool, error) {
	unit := p.serviceToSystemdUnit(service)

	output, err := p.runSystemctl("is-active", unit)
	if err != nil {
		trimmed := strings.TrimSpace(output)
		if isKnownInactiveOutput(trimmed) {
			return false, nil
		}
		return false, fmt.Errorf("systemctl is-active %s: %w", unit, err)
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

	if p.infra != nil {
		p.infra.InvalidateExposeCache(service)
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

	if p.infra != nil {
		p.infra.InvalidateExposeCache(service)
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
		if err != nil && !isKnownInactiveOutput(strings.TrimSpace(output)) {
			return fmt.Errorf("checking %s: %w", unit, err)
		}
		time.Sleep(polling)
	}

	return fmt.Errorf("timeout waiting for %s to be ready", unit)
}

func (p *ServiceLifecycleProvider) serviceToContainerName(service infra.ServiceName) string {
	return GetServiceInfo(service).ContainerName + ".container"
}

func quadletDropInDir(containerFile string) string {
	return fmt.Sprintf("/etc/containers/systemd/%s.d", containerFile)
}

func quadletDropInPath(containerFile string) string {
	return fmt.Sprintf("%s/e2e-env-override.conf", quadletDropInDir(containerFile))
}

// readDropIn reads the existing Quadlet drop-in file content. Returns empty string if the file doesn't exist.
func (p *ServiceLifecycleProvider) readDropIn(containerFile string) (string, error) {
	output, err := p.infra.RunCommand("cat", quadletDropInPath(containerFile))
	if err != nil {
		if strings.Contains(output, "No such file") {
			return "", nil
		}
		return "", fmt.Errorf("reading drop-in for %s: %w", containerFile, err)
	}
	return output, nil
}

// writeDropIn creates the Quadlet drop-in directory and writes the drop-in file.
func (p *ServiceLifecycleProvider) writeDropIn(containerFile, content string) error {
	if _, err := p.infra.RunCommand("mkdir", "-p", quadletDropInDir(containerFile)); err != nil {
		return fmt.Errorf("creating drop-in dir for %s: %w", containerFile, err)
	}
	if _, err := p.infra.runCommandWithStdin(strings.NewReader(content), "tee", quadletDropInPath(containerFile)); err != nil {
		return fmt.Errorf("writing drop-in for %s: %w", containerFile, err)
	}
	return nil
}

// removeDropIn removes the Quadlet drop-in file and its directory.
func (p *ServiceLifecycleProvider) removeDropIn(containerFile string) error {
	if _, err := p.infra.RunCommand("rm", "-f", quadletDropInPath(containerFile)); err != nil {
		return fmt.Errorf("removing drop-in for %s: %w", containerFile, err)
	}
	_, _ = p.infra.RunCommand("rmdir", quadletDropInDir(containerFile))
	return nil
}

// parseEnvLines parses Environment= lines from a drop-in file into a map.
func parseEnvLines(content string) map[string]string {
	envs := make(map[string]string)
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Environment=") {
			continue
		}
		// Format: Environment="KEY=VALUE"
		val := strings.TrimPrefix(line, "Environment=")
		val = strings.Trim(val, "\"")
		if idx := strings.Index(val, "="); idx > 0 {
			envs[val[:idx]] = val[idx+1:]
		}
	}
	return envs
}

func buildDropInContent(envs map[string]string) string {
	var b strings.Builder
	b.WriteString("[Container]\n")
	for k, v := range envs {
		fmt.Fprintf(&b, "Environment=%s=%s\n", k, v)
	}
	return b.String()
}

// daemonReloadAndRestart runs systemctl daemon-reload, restarts the unit, and waits for ready.
func (p *ServiceLifecycleProvider) daemonReloadAndRestart(service infra.ServiceName, unit string) error {
	if _, err := p.runSystemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload failed: %w", err)
	}
	if _, err := p.runSystemctl("restart", unit); err != nil {
		return fmt.Errorf("restart %s failed: %w", unit, err)
	}
	if p.infra != nil {
		p.infra.InvalidateExposeCache(service)
	}
	return p.WaitForReady(service, 5*time.Minute)
}

// SetDeploymentEnv sets an environment variable on a Quadlet service using a Quadlet container drop-in.
func (p *ServiceLifecycleProvider) SetDeploymentEnv(service infra.ServiceName, envName, envValue string) error {
	unit := p.serviceToSystemdUnit(service)
	containerFile := p.serviceToContainerName(service)

	existing, err := p.readDropIn(containerFile)
	if err != nil {
		return err
	}

	envs := parseEnvLines(existing)
	envs[envName] = envValue

	if err := p.writeDropIn(containerFile, buildDropInContent(envs)); err != nil {
		return err
	}

	logrus.Infof("Quadlet: set env %s=%s on %s", envName, envValue, containerFile)
	return p.daemonReloadAndRestart(service, unit)
}

// RemoveDeploymentEnv removes an environment variable from a Quadlet service's container drop-in.
func (p *ServiceLifecycleProvider) RemoveDeploymentEnv(service infra.ServiceName, envName string) error {
	unit := p.serviceToSystemdUnit(service)
	containerFile := p.serviceToContainerName(service)

	existing, err := p.readDropIn(containerFile)
	if err != nil {
		return err
	}

	envs := parseEnvLines(existing)
	delete(envs, envName)

	if len(envs) == 0 {
		if err := p.removeDropIn(containerFile); err != nil {
			return err
		}
	} else {
		if err := p.writeDropIn(containerFile, buildDropInContent(envs)); err != nil {
			return err
		}
	}

	logrus.Infof("Quadlet: removed env %s from %s", envName, containerFile)
	return p.daemonReloadAndRestart(service, unit)
}

// AreServicesHealthy checks if all flightctl services are healthy by checking the flightctl.target.
func (p *ServiceLifecycleProvider) AreServicesHealthy() (bool, error) {
	output, err := p.runSystemctl("is-active", "flightctl.target")
	if err != nil {
		trimmed := strings.TrimSpace(output)
		if isKnownInactiveOutput(trimmed) {
			logrus.Warnf("Quadlet: flightctl.target is not active")
			return false, nil
		}
		return false, fmt.Errorf("systemctl is-active flightctl.target: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(output), "active") {
		logrus.Warnf("Quadlet: flightctl.target is not active")
		return false, nil
	}

	logrus.Info("Quadlet: flightctl.target is healthy")
	return true, nil
}
