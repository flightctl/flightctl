// Package infra provides testcontainers-based infrastructure for E2E tests.
package infra

// ServiceName is a type-safe identifier for flightctl services.
type ServiceName string

// ServiceName constants for logical service names.
// Use these constants when calling InfraProvider methods that accept ServiceName.
const (
	ServiceRedis            ServiceName = "redis"
	ServiceAPI              ServiceName = "api"
	ServiceWorker           ServiceName = "worker"
	ServicePeriodic         ServiceName = "periodic"
	ServiceTelemetryGateway ServiceName = "telemetry-gateway"
	ServiceUI               ServiceName = "ui"
	ServiceDB               ServiceName = "db"
	ServiceAlertmanager     ServiceName = "alertmanager"
	ServiceImageBuilderAPI  ServiceName = "imagebuilder-api"
)

// InfraProvider abstracts infrastructure access for different deployment environments.
// K8s implementations use kubectl/client-go, Quadlet implementations use systemctl and files.
type InfraProvider interface {
	// GetConfigValue retrieves a configuration value by name and key.
	// For K8s: reads from ConfigMap
	// For Quadlet: reads from config files or environment variables
	GetConfigValue(name, key string) (string, error)

	// GetServiceConfig retrieves the full configuration content for a service.
	// For K8s: reads from ConfigMap data (e.g., config.yaml key)
	// For Quadlet: reads the config file from container or host
	GetServiceConfig(service ServiceName) (string, error)

	// GetSecretValue retrieves a secret value by name and key.
	// For K8s: reads from Secret (base64 decoded)
	// For Quadlet: reads from secret files or environment variables
	GetSecretValue(name, key string) (string, error)

	// GetServiceEndpoint returns the host and port for a named service.
	// For K8s: returns in-cluster DNS name and port (not directly accessible from test host)
	// For Quadlet: returns the configured host with the service's port
	GetServiceEndpoint(service ServiceName) (host string, port int, err error)

	// ExposeService makes an internal service accessible from the test host.
	// Use this for services not normally exposed externally (e.g., metrics endpoints).
	// For K8s: starts port-forwarding and returns localhost URL + cleanup function
	// For Quadlet: returns direct URL + no-op cleanup (internal services accessible on host)
	// The cleanup function must be called when done (e.g., defer cleanup()).
	ExposeService(service ServiceName, protocol string) (url string, cleanup func(), err error)

	// ExecInService executes a command in the context of a service.
	// For K8s: kubectl exec into the service's pod
	// For Quadlet: direct command execution or SSH
	ExecInService(service ServiceName, command []string) (string, error)

	// GetEnvironmentType returns the type of environment ("kind", "ocp", "quadlet").
	GetEnvironmentType() string

	// GetNamespace returns the namespace/context for the flightctl services.
	// For K8s: returns the Kubernetes namespace
	// For Quadlet: returns empty string or a logical grouping name
	GetNamespace() string
}

// EnvironmentType constants for environment detection.
const (
	EnvironmentKind    = "kind"
	EnvironmentOCP     = "ocp"
	EnvironmentQuadlet = "quadlet"
)
