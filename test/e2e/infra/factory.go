// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

import (
	"os"
	"os/exec"
	"strings"

	"github.com/sirupsen/logrus"
)

// Providers holds all the infrastructure providers for a test environment.
type Providers struct {
	Infra     InfraProvider
	Lifecycle ServiceLifecycleProvider
	RBAC      RBACProvider
}

// EnvironmentConfig holds configuration for the test environment.
// Values can be set via environment variables or programmatically.
type EnvironmentConfig struct {
	// Type is the environment type: "kind", "ocp", or "quadlet"
	Type string

	// Namespace is the K8s namespace for flightctl services (K8s only)
	Namespace string

	// APIEndpoint is the FlightCtl API endpoint URL (e.g., "https://api.flightctl.example.com:3443")
	APIEndpoint string

	// KubeConfig is the path to kubeconfig file (K8s only, defaults to ~/.kube/config)
	KubeConfig string

	// KubeContext is the kubeconfig context to use (K8s only)
	KubeContext string

	// SSHHost is the remote host for Quadlet deployments (Quadlet only, for remote testing)
	SSHHost string

	// SSHUser is the SSH username for remote Quadlet deployments
	SSHUser string

	// SSHKeyPath is the path to SSH private key for remote Quadlet deployments
	SSHKeyPath string

	// UseSudo indicates whether to use sudo for systemctl/podman commands (Quadlet only)
	UseSudo bool

	// ConfigDir is the config directory for Quadlet deployments (defaults to /etc/flightctl)
	ConfigDir string
}

// Environment variable names for configuration
const (
	EnvE2EEnvironment = "E2E_ENVIRONMENT"  // Environment type: k8s, ocp, quadlet
	EnvE2ENamespace   = "E2E_NAMESPACE"    // K8s namespace
	EnvE2EAPIEndpoint = "E2E_API_ENDPOINT" // FlightCtl API endpoint URL
	EnvKubeConfig     = "KUBECONFIG"       // Kubeconfig path
	EnvE2EKubeContext = "E2E_KUBE_CONTEXT" // Kubeconfig context
	EnvE2ESSHHost     = "E2E_SSH_HOST"     // Remote SSH host for Quadlet
	EnvE2ESSHUser     = "E2E_SSH_USER"     // SSH username
	EnvE2ESSHKeyPath  = "E2E_SSH_KEY_PATH" // SSH key path
	EnvE2EUseSudo     = "E2E_USE_SUDO"     // Use sudo (default: true for quadlet)
	EnvE2EConfigDir   = "E2E_CONFIG_DIR"   // Config directory for Quadlet
)

// Default values for different environments
const (
	// DefaultQuadletConfigDir is the default config directory for Quadlet deployments
	DefaultQuadletConfigDir = "/etc/flightctl"
	// DefaultK8sNamespace is the default namespace for K8s deployments
	DefaultK8sNamespace = "flightctl"
	// DefaultAPIPort is the default port for the FlightCtl API
	DefaultAPIPort = "3443"
)

// GetDefaultQuadletAPIEndpoint returns the default API endpoint for Quadlet deployments.
// Uses the host's FQDN (hostname -f). VMs will have /etc/hosts entry injected during prepare.
func GetDefaultQuadletAPIEndpoint() string {
	// Try to get FQDN first
	cmd := exec.Command("hostname", "-f")
	output, err := cmd.Output()
	if err == nil {
		hostname := strings.TrimSpace(string(output))
		if hostname != "" && hostname != "localhost" {
			return "https://" + hostname + ":" + DefaultAPIPort
		}
	}

	// Fallback to IP address if FQDN not available
	hostIP := GetHostIP()
	if hostIP != "" {
		return "https://" + hostIP + ":" + DefaultAPIPort
	}

	// Last resort fallback
	return "https://localhost:" + DefaultAPIPort
}

// GetEnvironmentConfig reads configuration from environment variables.
func GetEnvironmentConfig() *EnvironmentConfig {
	config := &EnvironmentConfig{
		Type:        os.Getenv(EnvE2EEnvironment),
		Namespace:   os.Getenv(EnvE2ENamespace),
		APIEndpoint: os.Getenv(EnvE2EAPIEndpoint),
		KubeConfig:  os.Getenv(EnvKubeConfig),
		KubeContext: os.Getenv(EnvE2EKubeContext),
		SSHHost:     os.Getenv(EnvE2ESSHHost),
		SSHUser:     os.Getenv(EnvE2ESSHUser),
		SSHKeyPath:  os.Getenv(EnvE2ESSHKeyPath),
		ConfigDir:   os.Getenv(EnvE2EConfigDir),
	}

	// Parse UseSudo (default: true for quadlet, false otherwise)
	if useSudo := os.Getenv(EnvE2EUseSudo); useSudo != "" {
		config.UseSudo = strings.ToLower(useSudo) == "true" || useSudo == "1"
	} else {
		// Default to true for quadlet environments
		config.UseSudo = true
	}

	// Normalize environment type
	if config.Type != "" {
		config.Type = normalizeEnvironmentType(config.Type)
	}

	return config
}

// GetAPIEndpoint returns the API endpoint, using defaults if not explicitly set.
// For K8s: checks API_ENDPOINT env var (set by run_e2e_tests.sh from K8s route)
// For Quadlet: defaults to https://<host-ip>:3443 (uses host IP so VMs can reach it)
func (c *EnvironmentConfig) GetAPIEndpoint() string {
	// First check our config
	if c.APIEndpoint != "" {
		return c.APIEndpoint
	}

	// Check the legacy API_ENDPOINT env var (set by run_e2e_tests.sh)
	if ep := os.Getenv("API_ENDPOINT"); ep != "" {
		return ep
	}

	// Apply defaults based on environment type
	envType := c.Type
	if envType == "" {
		envType = DetectEnvironment()
	}

	if envType == EnvironmentQuadlet {
		return GetDefaultQuadletAPIEndpoint()
	}

	// For K8s, no default - must be provided (usually by run_e2e_tests.sh)
	return ""
}

// GetConfigDir returns the config directory, using defaults if not set.
func (c *EnvironmentConfig) GetConfigDir() string {
	if c.ConfigDir != "" {
		return c.ConfigDir
	}
	return DefaultQuadletConfigDir
}

// GetNamespace returns the namespace, using defaults if not set.
func (c *EnvironmentConfig) GetNamespace() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return DefaultK8sNamespace
}

// IsRemote returns true if this is a remote deployment (has SSHHost or external API endpoint).
func (c *EnvironmentConfig) IsRemote() bool {
	return c.SSHHost != "" || c.APIEndpoint != ""
}

// ProviderFactory creates providers based on environment detection.
type ProviderFactory struct {
	// Cached environment type
	envType string
	// Configuration
	config *EnvironmentConfig
}

// NewProviderFactory creates a new provider factory.
func NewProviderFactory() *ProviderFactory {
	return &ProviderFactory{
		config: GetEnvironmentConfig(),
	}
}

// NewProviderFactoryWithConfig creates a provider factory with explicit configuration.
func NewProviderFactoryWithConfig(config *EnvironmentConfig) *ProviderFactory {
	return &ProviderFactory{
		config: config,
	}
}

// GetConfig returns the environment configuration.
func (f *ProviderFactory) GetConfig() *EnvironmentConfig {
	return f.config
}

// DetectEnvironment determines the current environment type.
// Priority: E2E_ENVIRONMENT env var > auto-detection
func (f *ProviderFactory) DetectEnvironment() string {
	if f.envType != "" {
		return f.envType
	}

	// Check config (which reads from env var)
	if f.config != nil && f.config.Type != "" {
		f.envType = f.config.Type
		logrus.Infof("Environment from config: %s", f.envType)
		return f.envType
	}

	// Auto-detect
	f.envType = f.autoDetect()
	logrus.Infof("Environment auto-detected: %s", f.envType)
	return f.envType
}

// normalizeEnvironmentType normalizes environment type strings.
func normalizeEnvironmentType(envType string) string {
	envType = strings.ToLower(strings.TrimSpace(envType))
	switch envType {
	case "k8s", "kubernetes", "kind":
		return EnvironmentKind
	case "ocp", "openshift":
		return EnvironmentOCP
	case "quadlet":
		return EnvironmentQuadlet
	default:
		return envType
	}
}

// autoDetect attempts to automatically detect the environment.
func (f *ProviderFactory) autoDetect() string {
	// Check if kubectl is available and working
	cmd := exec.Command("kubectl", "config", "current-context")
	output, err := cmd.Output()
	if err == nil {
		context := strings.TrimSpace(string(output))
		logrus.Debugf("kubectl context: %s", context)

		// Check for KIND
		if strings.Contains(context, "kind") {
			return EnvironmentKind
		}

		// Check if it's OpenShift by looking for OpenShift-specific APIs
		cmd = exec.Command("kubectl", "api-resources", "--api-group=route.openshift.io")
		if err := cmd.Run(); err == nil {
			return EnvironmentOCP
		}

		// Default to KIND for any other K8s cluster
		return EnvironmentKind
	}

	// kubectl not available or not configured, check for systemd/quadlet
	cmd = exec.Command("systemctl", "is-active", "flightctl-api.service")
	if err := cmd.Run(); err == nil {
		return EnvironmentQuadlet
	}

	// Check with sudo
	cmd = exec.Command("sudo", "systemctl", "is-active", "flightctl-api.service")
	if err := cmd.Run(); err == nil {
		return EnvironmentQuadlet
	}

	// Default to KIND if nothing detected
	logrus.Warn("Could not auto-detect environment, defaulting to KIND")
	return EnvironmentKind
}

// IsK8sEnvironment returns true if the current environment is Kubernetes-based.
func (f *ProviderFactory) IsK8sEnvironment() bool {
	env := f.DetectEnvironment()
	return env == EnvironmentKind || env == EnvironmentOCP
}

// IsQuadletEnvironment returns true if the current environment is Quadlet-based.
func (f *ProviderFactory) IsQuadletEnvironment() bool {
	return f.DetectEnvironment() == EnvironmentQuadlet
}

// Global factory instance for convenience
var defaultFactory = NewProviderFactory()

// DetectEnvironment returns the detected environment type using the default factory.
func DetectEnvironment() string {
	return defaultFactory.DetectEnvironment()
}

// IsK8sEnvironment returns true if running in a K8s environment.
func IsK8sEnvironment() bool {
	return defaultFactory.IsK8sEnvironment()
}

// IsQuadletEnvironment returns true if running in a Quadlet environment.
func IsQuadletEnvironment() bool {
	return defaultFactory.IsQuadletEnvironment()
}

// GetConfig returns the environment configuration from the default factory.
func GetConfig() *EnvironmentConfig {
	return defaultFactory.GetConfig()
}

// IsRemoteDeployment returns true if testing against a remote deployment.
func IsRemoteDeployment() bool {
	return defaultFactory.GetConfig().IsRemote()
}

// GetAPIEndpoint returns the API endpoint using the default factory's config.
// Uses defaults if not explicitly configured.
func GetAPIEndpoint() string {
	return defaultFactory.GetConfig().GetAPIEndpoint()
}
