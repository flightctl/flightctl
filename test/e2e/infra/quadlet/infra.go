// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"sigs.k8s.io/yaml"
)

// ServiceInfo holds metadata about a Quadlet service.
type ServiceInfo struct {
	ContainerName string
	SystemdUnit   string
	Port          int
}

// ServiceRegistry maps service names to their Quadlet-specific metadata.
var ServiceRegistry = map[infra.ServiceName]ServiceInfo{
	infra.ServiceRedis:            {ContainerName: "flightctl-kv", SystemdUnit: "flightctl-kv.service", Port: 6379},
	infra.ServiceAPI:              {ContainerName: "flightctl-api", SystemdUnit: "flightctl-api.service", Port: 3443},
	infra.ServiceWorker:           {ContainerName: "flightctl-worker", SystemdUnit: "flightctl-worker.service", Port: 7443},
	infra.ServicePeriodic:         {ContainerName: "flightctl-periodic", SystemdUnit: "flightctl-periodic.service", Port: 0},
	infra.ServiceTelemetryGateway: {ContainerName: "flightctl-telemetry-gateway", SystemdUnit: "flightctl-telemetry-gateway.service", Port: 9464},
	infra.ServiceDB:               {ContainerName: "flightctl-db", SystemdUnit: "flightctl-db.service", Port: 5432},
	infra.ServiceUI:               {ContainerName: "flightctl-ui", SystemdUnit: "flightctl-ui.service", Port: 9001},
	infra.ServiceAlertmanager:     {ContainerName: "flightctl-alertmanager", SystemdUnit: "flightctl-alertmanager.service", Port: 9093},
	infra.ServiceImageBuilderAPI:  {ContainerName: "flightctl-imagebuilder-api", SystemdUnit: "flightctl-imagebuilder-api.service", Port: 8445},
}

// InfraProvider implements infra.InfraProvider for Quadlet environments.
// Supports both local and remote Quadlet deployments via SSH.
type InfraProvider struct {
	// host is the hostname/IP where Quadlet services are running
	host string
	// sshUser is the SSH user for remote connections (empty for local)
	sshUser string
	// sshKeyPath is the path to SSH private key (optional)
	sshKeyPath string
	// configDir is the base directory for config files on the target host
	configDir string
	// secretDir is the base directory for secret files on the target host
	secretDir string
	// useSudo indicates whether to use sudo for commands on the target host
	useSudo bool
}

// NewInfraProvider creates a new Quadlet InfraProvider.
// For remote hosts, set QUADLET_HOST and QUADLET_SSH_USER env vars.
// Optionally set QUADLET_SSH_KEY for the SSH private key path.
func NewInfraProvider(configDir, secretDir string, useSudo bool) *InfraProvider {
	host := os.Getenv("QUADLET_HOST")
	if host == "" {
		host = "localhost"
	}
	if configDir == "" {
		configDir = "/etc/flightctl"
	}
	if secretDir == "" {
		secretDir = "/etc/flightctl/secrets" //nolint:gosec // G101: This is a path, not credentials
	}
	return &InfraProvider{
		host:       host,
		sshUser:    os.Getenv("QUADLET_SSH_USER"),
		sshKeyPath: os.Getenv("QUADLET_SSH_KEY"),
		configDir:  configDir,
		secretDir:  secretDir,
		useSudo:    useSudo,
	}
}

// GetHost returns the host where Quadlet services are running.
func (p *InfraProvider) GetHost() string {
	return p.host
}

// isRemote returns true if the Quadlet host is remote (requires SSH).
func (p *InfraProvider) isRemote() bool {
	return p.sshUser != "" && p.host != "localhost" && p.host != "127.0.0.1"
}

// runCommand executes a command, using SSH if the host is remote.
func (p *InfraProvider) runCommand(command ...string) (string, error) {
	var cmd *exec.Cmd

	if p.isRemote() {
		// Build SSH command
		sshArgs := []string{"-o", "StrictHostKeyChecking=no", "-o", "BatchMode=yes"}
		if p.sshKeyPath != "" {
			sshArgs = append(sshArgs, "-i", p.sshKeyPath)
		}
		sshTarget := fmt.Sprintf("%s@%s", p.sshUser, p.host)
		sshArgs = append(sshArgs, sshTarget)

		// Build remote command with optional sudo
		remoteCmd := strings.Join(command, " ")
		if p.useSudo {
			remoteCmd = "sudo " + remoteCmd
		}
		sshArgs = append(sshArgs, remoteCmd)

		cmd = exec.Command("ssh", sshArgs...)
	} else {
		// Local execution
		if p.useSudo {
			cmd = exec.Command("sudo", command...)
		} else {
			cmd = exec.Command(command[0], command[1:]...) //nolint:gosec // G204: command args are from internal test config
		}
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// GetConfigValue retrieves a configuration value from config files or environment.
func (p *InfraProvider) GetConfigValue(name, key string) (string, error) {
	// Try environment variable first (uppercase, with underscores)
	envKey := strings.ToUpper(strings.ReplaceAll(name+"_"+key, "-", "_"))
	if value := os.Getenv(envKey); value != "" {
		return value, nil
	}

	// Read config file from target host
	configFile := filepath.Join(p.configDir, name+".yaml")
	data, err := p.runCommand("cat", configFile)
	if err != nil {
		// Try without .yaml extension
		configFile = filepath.Join(p.configDir, name)
		data, err = p.runCommand("cat", configFile)
		if err != nil {
			return "", fmt.Errorf("config %s not found in environment or files: %w", name, err)
		}
	}

	// Parse as YAML and extract key
	var config map[string]interface{}
	if err := yaml.Unmarshal([]byte(data), &config); err != nil {
		// If not YAML, treat as plain text for simple key lookup
		return data, nil
	}

	value, ok := config[key]
	if !ok {
		return "", fmt.Errorf("key %s not found in config %s", key, name)
	}

	return fmt.Sprintf("%v", value), nil
}

// GetServiceConfig retrieves the full configuration content for a service.
// For Quadlet, configs are on the target host at /etc/flightctl/<container-name>/config.yaml.
func (p *InfraProvider) GetServiceConfig(service infra.ServiceName) (string, error) {
	hostPath := p.serviceToHostConfigPath(service)
	output, err := p.runCommand("cat", hostPath)
	if err != nil {
		return "", fmt.Errorf("failed to read config for service %s from %s: %w", service, hostPath, err)
	}
	return output, nil
}

// serviceToHostConfigPath returns the host filesystem path for a service's config.
// Quadlet configs are stored at /etc/flightctl/<container-name>/config.yaml on the host.
func (p *InfraProvider) serviceToHostConfigPath(service infra.ServiceName) string {
	info := GetServiceInfo(service)
	return filepath.Join(p.configDir, info.ContainerName, "config.yaml")
}

// GetSecretValue retrieves a secret value from secret files or environment.
func (p *InfraProvider) GetSecretValue(name, key string) (string, error) {
	// Try environment variable first
	envKey := strings.ToUpper(strings.ReplaceAll(name+"_"+key, "-", "_"))
	if value := os.Getenv(envKey); value != "" {
		return value, nil
	}

	// Try specific key file in secrets directory on target host
	secretFile := filepath.Join(p.secretDir, name, key)
	data, err := p.runCommand("cat", secretFile)
	if err == nil {
		return strings.TrimSpace(data), nil
	}

	// Try YAML file
	secretFile = filepath.Join(p.secretDir, name+".yaml")
	data, err = p.runCommand("cat", secretFile)
	if err != nil {
		return "", fmt.Errorf("secret %s/%s not found in environment or files: %w", name, key, err)
	}

	var secrets map[string]interface{}
	if err := yaml.Unmarshal([]byte(data), &secrets); err != nil {
		return "", fmt.Errorf("failed to parse secret file %s: %w", secretFile, err)
	}

	value, ok := secrets[key]
	if !ok {
		return "", fmt.Errorf("key %s not found in secret %s", key, name)
	}

	return fmt.Sprintf("%v", value), nil
}

// GetServiceEndpoint returns the host and port for a named service.
// For Quadlet, services run on the configured host with well-known ports.
func (p *InfraProvider) GetServiceEndpoint(service infra.ServiceName) (string, int, error) {
	info := GetServiceInfo(service)
	return p.host, info.Port, nil
}

// ExposeService makes an internal service accessible from the test host.
// This is for services that are not normally exposed externally (e.g., metrics endpoints).
// For Quadlet, internal services are directly accessible on the host, so this just builds the URL.
// For K8s, this would start port-forwarding.
func (p *InfraProvider) ExposeService(service infra.ServiceName, protocol string) (string, func(), error) {
	host, port, err := p.GetServiceEndpoint(service)
	if err != nil {
		return "", nil, err
	}
	url := fmt.Sprintf("%s://%s:%d", protocol, host, port)
	cleanup := func() {} // no-op for Quadlet - services already accessible
	return url, cleanup, nil
}

// ExecInService executes a command in the context of a service container.
// For Quadlet, this uses podman exec on the target host (local or remote via SSH).
func (p *InfraProvider) ExecInService(service infra.ServiceName, command []string) (string, error) {
	info := GetServiceInfo(service)

	podmanArgs := append([]string{"podman", "exec", info.ContainerName}, command...)
	output, err := p.runCommand(podmanArgs...)
	if err != nil {
		return output, fmt.Errorf("failed to exec in service %s: %w", service, err)
	}
	return output, nil
}

// getServiceInfo returns the service metadata from the registry.
// GetServiceInfo returns the service metadata from the registry.
func GetServiceInfo(service infra.ServiceName) ServiceInfo {
	if info, ok := ServiceRegistry[service]; ok {
		return info
	}
	// Default for unknown services
	name := string(service)
	return ServiceInfo{ContainerName: name, SystemdUnit: name + ".service", Port: 0}
}

// GetEnvironmentType returns "quadlet".
func (p *InfraProvider) GetEnvironmentType() string {
	return infra.EnvironmentQuadlet
}

// GetNamespace returns empty string for Quadlet (no namespace concept).
func (p *InfraProvider) GetNamespace() string {
	return ""
}
