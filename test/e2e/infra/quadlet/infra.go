// Package quadlet provides Quadlet/systemd-specific implementations of the infra providers.
package quadlet

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/sirupsen/logrus"
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
	infra.ServiceRedis:              {ContainerName: "flightctl-kv", SystemdUnit: "flightctl-kv.service", Port: 6379},
	infra.ServiceAPI:                {ContainerName: "flightctl-api", SystemdUnit: "flightctl-api.service", Port: 3443},
	infra.ServiceWorker:             {ContainerName: "flightctl-worker", SystemdUnit: "flightctl-worker.service", Port: 7443},
	infra.ServicePeriodic:           {ContainerName: "flightctl-periodic", SystemdUnit: "flightctl-periodic.service", Port: 0},
	infra.ServiceTelemetryGateway:   {ContainerName: "flightctl-telemetry-gateway", SystemdUnit: "flightctl-telemetry-gateway.service", Port: 9464},
	infra.ServiceDB:                 {ContainerName: "flightctl-db", SystemdUnit: "flightctl-db.service", Port: 5432},
	infra.ServiceUI:                 {ContainerName: "flightctl-ui", SystemdUnit: "flightctl-ui.service", Port: 9001},
	infra.ServiceAlertmanager:       {ContainerName: "flightctl-alertmanager", SystemdUnit: "flightctl-alertmanager.service", Port: 9093},
	infra.ServiceAlertmanagerProxy:  {ContainerName: "flightctl-alertmanager-proxy", SystemdUnit: "flightctl-alertmanager-proxy.service", Port: 8443},
	infra.ServiceImageBuilderAPI:    {ContainerName: "flightctl-imagebuilder-api", SystemdUnit: "flightctl-imagebuilder-api.service", Port: 8445},
	infra.ServiceImageBuilderWorker: {ContainerName: "flightctl-imagebuilder-worker", SystemdUnit: "flightctl-imagebuilder-worker.service", Port: 8080},
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

	// exposeCache keeps one port-forward per (service, protocol) so repeated ExposeService calls reuse it.
	exposeCache map[string]struct {
		url     string
		cleanup func()
	}
	exposeCacheMu sync.Mutex
}

// NewInfraProvider creates a new Quadlet InfraProvider.
// For remote hosts, set QUADLET_HOST and QUADLET_SSH_USER env vars.
// Optionally set QUADLET_SSH_KEY for the SSH private key path.
// Registry comes from satellite; use satellite.Get(ctx).RegistryHost/RegistryPort and pass explicitly.
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

// RunCommand runs a command on the Quadlet host (with SSH/sudo if configured). Used by SecretsProvider to read Podman secrets via container env.
func (p *InfraProvider) RunCommand(command ...string) (string, error) {
	return p.runCommand(command...)
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
// If the container already publishes the port (e.g. flightctl-api), returns that URL and a no-op cleanup.
// Otherwise (e.g. Redis) starts a TCP forwarder from a local port to the container.
// Repeated calls for the same (service, protocol) return the same URL so polling (e.g. WaitForQueueAccessible) does not spawn a new forward every time.
// For remote Quadlet, returns host:port and the deployment must expose the service.
func (p *InfraProvider) ExposeService(service infra.ServiceName, protocol string) (string, func(), error) {
	info := GetServiceInfo(service)
	if info.Port == 0 {
		return "", nil, fmt.Errorf("ExposeService: service %s has no port", service)
	}
	if p.isRemote() {
		host, port, err := p.GetServiceEndpoint(service)
		if err != nil {
			return "", nil, err
		}
		return fmt.Sprintf("%s://%s:%d", protocol, host, port), func() {}, nil
	}
	cacheKey := string(service) + ":" + protocol
	p.exposeCacheMu.Lock()
	if p.exposeCache != nil {
		if e, ok := p.exposeCache[cacheKey]; ok {
			p.exposeCacheMu.Unlock()
			return e.url, func() {}, nil
		}
	} else {
		p.exposeCache = make(map[string]struct {
			url     string
			cleanup func()
		})
	}
	p.exposeCacheMu.Unlock()

	// Local Quadlet: if port is already published, use it (no-op).
	if hostPort, ok := p.getPublishedPort(info.ContainerName, info.Port); ok {
		host := p.host
		if host == "localhost" {
			host = "127.0.0.1"
		}
		url := fmt.Sprintf("%s://%s:%d", protocol, host, hostPort)
		p.exposeCacheMu.Lock()
		if e, ok := p.exposeCache[cacheKey]; ok {
			p.exposeCacheMu.Unlock()
			return e.url, func() {}, nil
		}
		p.exposeCache[cacheKey] = struct {
			url     string
			cleanup func()
		}{url, func() {}}
		p.exposeCacheMu.Unlock()
		return url, func() {}, nil
	}
	// Not published: port-forward from a local port to the container.
	containerIP, err := p.getContainerIP(info.ContainerName)
	if err != nil {
		return "", nil, fmt.Errorf("get container IP for %s: %w", info.ContainerName, err)
	}
	localPort, err := getFreePort()
	if err != nil {
		return "", nil, fmt.Errorf("get free port: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(localPort))
	if err != nil {
		return "", nil, fmt.Errorf("listen on 127.0.0.1:%d: %w", localPort, err)
	}
	target := net.JoinHostPort(containerIP, strconv.Itoa(info.Port))
	go runTCPForward(listener, target, service)
	url := fmt.Sprintf("%s://127.0.0.1:%d", protocol, localPort)
	cleanup := func() { _ = listener.Close() }
	p.exposeCacheMu.Lock()
	if e, ok := p.exposeCache[cacheKey]; ok {
		p.exposeCacheMu.Unlock()
		_ = listener.Close()
		return e.url, func() {}, nil
	}
	p.exposeCache[cacheKey] = struct {
		url     string
		cleanup func()
	}{url, cleanup}
	p.exposeCacheMu.Unlock()
	logrus.Infof("Quadlet: port-forwarding %s to %s", service, url)
	// Return no-op cleanup so callers (e.g. GetRedisClient) do not close the shared forward when they're done.
	return url, func() {}, nil
}

// InvalidateExposeCache closes any cached port-forward for the service and removes it from the cache.
// Call after restarting a service so the next ExposeService creates a new forward to the new container IP.
func (p *InfraProvider) InvalidateExposeCache(service infra.ServiceName) {
	prefix := string(service) + ":"
	p.exposeCacheMu.Lock()
	defer p.exposeCacheMu.Unlock()
	if p.exposeCache == nil {
		return
	}
	for key, e := range p.exposeCache {
		if strings.HasPrefix(key, prefix) {
			e.cleanup()
			delete(p.exposeCache, key)
		}
	}
}

// getPublishedPort returns the host port if the container publishes the given container port (e.g. 0.0.0.0:3443 -> 3443).
// Returns (0, false) if the port is not published.
func (p *InfraProvider) getPublishedPort(containerName string, containerPort int) (hostPort int, ok bool) {
	portArg := fmt.Sprintf("%d/tcp", containerPort)
	args := []string{"podman", "port", containerName, portArg}
	output, err := p.runCommand(args...)
	if err != nil {
		return 0, false
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return 0, false
	}
	// Output is like "0.0.0.0:3443" or ":::3443" -> take the part after the last colon.
	if i := strings.LastIndex(output, ":"); i >= 0 && i < len(output)-1 {
		var p int
		if _, err := fmt.Sscanf(output[i+1:], "%d", &p); err == nil {
			return p, true
		}
	}
	return 0, false
}

// getContainerIP returns the first container IP (Podman bridge).
func (p *InfraProvider) getContainerIP(containerName string) (string, error) {
	format := "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}"
	args := []string{"podman", "inspect", "-f", format, containerName}
	output, err := p.runCommand(args...)
	if err != nil {
		return "", err
	}
	ip := strings.TrimSpace(output)
	if ip == "" {
		return "", fmt.Errorf("container %s has no network IP", containerName)
	}
	return ip, nil
}

func getFreePort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

// runTCPForward accepts on listener and forwards each connection to target (host:port).
func runTCPForward(listener net.Listener, target string, service infra.ServiceName) {
	for {
		client, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer client.Close()
			backend, err := net.Dial("tcp", target)
			if err != nil {
				logrus.Warnf("Quadlet forward %s: dial %s: %v", service, target, err)
				return
			}
			defer backend.Close()
			go func() { _, _ = io.Copy(backend, client) }()
			_, _ = io.Copy(client, backend)
		}()
	}
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

// GetAPILoginToken returns a token for flightctl login. Quadlet: read from env or file.
func (p *InfraProvider) GetAPILoginToken() (string, error) {
	if t := os.Getenv("E2E_PAM_TOKEN"); t != "" {
		return strings.TrimSpace(t), nil
	}
	tokenPath := filepath.Join(p.secretDir, "api-login.token")
	output, err := p.runCommand("cat", tokenPath)
	if err != nil {
		return "", fmt.Errorf("Quadlet: no E2E_PAM_TOKEN and could not read %s: %w", tokenPath, err)
	}
	return strings.TrimSpace(output), nil
}

// SetServiceConfig writes the config key content to the service's config file on the host.
func (p *InfraProvider) SetServiceConfig(service infra.ServiceName, configKey, content string) error {
	if configKey != "config.yaml" {
		return fmt.Errorf("Quadlet SetServiceConfig only supports config.yaml key, got %q", configKey)
	}
	hostPath := p.serviceToHostConfigPath(service)
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	escaped := strings.ReplaceAll(b64, "'", "'\"'\"'")
	script := fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", escaped, hostPath)
	_, err := p.runCommand("sh", "-c", script)
	if err != nil {
		return fmt.Errorf("write config to %s: %w", hostPath, err)
	}
	return nil
}
