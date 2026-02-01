// Package infra provides testcontainers-based infrastructure for E2E tests.
//
// Convention: never use exec kubectl or the raw Kubernetes client outside of
// infra/k8s. If harness, util, or other e2e code needs cluster or config access,
// that functionality must live in infra: implement it in infra/k8s (and in
// infra/quadlet with equivalent behaviour when relevant for both deployment types),
// expose it via InfraProvider or another infra interface, and have callers use
// the provider (e.g. from setup.GetDefaultProviders() or the harness).
package infra

// ServiceName is a type-safe identifier for flightctl services.
type ServiceName string

// ServiceName constants for logical service names.
// Use these constants when calling InfraProvider methods that accept ServiceName.
const (
	ServiceRedis              ServiceName = "redis"
	ServiceAPI                ServiceName = "api"
	ServiceWorker             ServiceName = "worker"
	ServicePeriodic           ServiceName = "periodic"
	ServiceTelemetryGateway   ServiceName = "telemetry-gateway"
	ServiceUI                 ServiceName = "ui"
	ServiceDB                 ServiceName = "db"
	ServiceAlertmanager       ServiceName = "alertmanager"
	ServiceAlertmanagerProxy  ServiceName = "alertmanager-proxy"
	ServiceImageBuilderAPI    ServiceName = "imagebuilder-api"
	ServiceImageBuilderWorker ServiceName = "imagebuilder-worker"
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

	// GetAPILoginToken returns a token suitable for flightctl login --token.
	// For K8s/KIND: kubectl create token in main namespace; OCP: oc whoami -t.
	// For Quadlet: read from file or return error. Namespace is internal to the provider.
	GetAPILoginToken() (string, error)

	// SetServiceConfig writes back config for a service (e.g. config.yaml in ConfigMap).
	// For K8s: updates ConfigMap data[configKey]; Quadlet: writes config file.
	SetServiceConfig(service ServiceName, configKey, content string) error
}

// DeploymentServiceNames maps deployment/service names (same in K8s and Quadlet) to ServiceName.
// Use when callers have a string (e.g. "flightctl-ui") and need a ServiceName for provider calls.
var DeploymentServiceNames = map[string]ServiceName{
	"flightctl-kv":                  ServiceRedis,
	"flightctl-api":                 ServiceAPI,
	"flightctl-worker":              ServiceWorker,
	"flightctl-periodic":            ServicePeriodic,
	"flightctl-telemetry-gateway":   ServiceTelemetryGateway,
	"flightctl-db":                  ServiceDB,
	"flightctl-ui":                  ServiceUI,
	"flightctl-alertmanager":        ServiceAlertmanager,
	"flightctl-alertmanager-proxy":  ServiceAlertmanagerProxy,
	"flightctl-imagebuilder-api":    ServiceImageBuilderAPI,
	"flightctl-imagebuilder-worker": ServiceImageBuilderWorker,
}

// ServiceNameFromDeploymentName returns the ServiceName for a deployment/service name string, or false if unknown.
func ServiceNameFromDeploymentName(name string) (ServiceName, bool) {
	s, ok := DeploymentServiceNames[name]
	return s, ok
}

// EnvironmentType constants for environment detection.
const (
	EnvironmentKind    = "kind"
	EnvironmentOCP     = "ocp"
	EnvironmentQuadlet = "quadlet"
)
