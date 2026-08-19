// Package infra provides testcontainers-based infrastructure for E2E tests.
//
// Convention: never use exec kubectl or the raw Kubernetes client outside of
// infra/k8s. If harness, util, or other e2e code needs cluster or config access,
// that functionality must live in infra: implement it in infra/k8s (and in
// infra/quadlet with equivalent behaviour when relevant for both deployment types),
// expose it via InfraProvider or another infra interface, and have callers use
// the provider (e.g. from setup.GetDefaultProviders() or the harness).
package infra

import (
	"context"
	"io"

	internalconfig "github.com/flightctl/flightctl/internal/config"
)

// DBConnectionParams holds the parameters needed to connect to the flightctl PostgreSQL database.
// Used by QueryDBExternal to reach an external DB when the built-in flightctl-db pod is unavailable.
type DBConnectionParams struct {
	Hostname    string
	Port        string
	DBName      string
	User        string
	Password    string
	SSLMode     string
	SSLRootCert string // path to the CA cert file on the deployment host (may be remote)
	SSLCert     string // path to the client cert file on the deployment host
	SSLKey      string // path to the client key file on the deployment host
}

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
	ServiceAlertExporter      ServiceName = "alert-exporter"
	ServicePrometheus         ServiceName = "prometheus"
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

	// InvalidateExposeCache closes any cached port-forward for the service and removes it from the cache.
	// Call before retrying ExposeService so the next call creates a new port-forward (e.g. after Redis restart).
	InvalidateExposeCache(service ServiceName)

	// ExecInService executes a command in the context of a service.
	// For K8s: kubectl exec into the service's pod
	// For Quadlet: direct command execution or SSH
	ExecInService(service ServiceName, command []string) (string, error)

	// ExecInServiceWithStdin executes a command in the context of a service with stdin attached.
	// Used e.g. to pipe a backup file into psql during DB restore.
	// For K8s: kubectl exec -i into the service's pod
	// For Quadlet: podman exec -i (and stdin piped through SSH when remote)
	ExecInServiceWithStdin(service ServiceName, command []string, stdin io.Reader) (string, error)

	// GetEnvironmentType returns the type of environment ("kind", "ocp", "quadlet").
	GetEnvironmentType() string

	// GetAPILoginToken returns a token suitable for flightctl login --token.
	// For K8s/KIND: kubectl create token in main namespace; OCP: oc whoami -t.
	// For Quadlet: read from file or return error. Namespace is internal to the provider.
	GetAPILoginToken() (string, error)

	// SetServiceConfig writes back config for a service (e.g. config.yaml in ConfigMap).
	// For K8s: updates ConfigMap data[configKey]. For Quadlet: single config file per
	// service; configKey is ignored (callers may pass "" or "config.yaml").
	SetServiceConfig(service ServiceName, configKey, content string) error

	// GetInternalNamespace returns the namespace where internal services (worker, db, kv, etc.) run.
	// For K8s: e.g. flightctl-internal when using split namespaces; empty when all in one namespace.
	// For Quadlet: empty.
	GetInternalNamespace() string

	// GetExternalNamespace returns the namespace where external services (API, UI, etc.) run (release namespace).
	// For K8s: the namespace where flightctl-api is deployed; used e.g. for io.flightctl/instance label.
	// For Quadlet: empty.
	GetExternalNamespace() string

	// BuiltinDatabaseWorkloadAvailable reports whether backup/restore tests can pg_dump/psql via the
	// built-in DB workload: K8s checks for a flightctl-db pod (Helm db.type=builtin); external Helm DB
	// has no such pod. Quadlet reads db.type from service-config.yaml (deploy/podman/service-config.yaml);
	// db.type=external uses an external Postgres instance instead of the flightctl-db container.
	BuiltinDatabaseWorkloadAvailable() bool

	// ServiceExists reports whether the deployment-specific resource for a logical service is present.
	// For K8s: checks the Service object exists. For Quadlet: checks the systemd unit is active.
	ServiceExists(ctx context.Context, service ServiceName) (bool, error)

	// SetEncryptionKey writes a named encryption key so it is available to the given service.
	// For K8s: patches the flightctl-encryption-key Secret to add/update the key entry, then
	// triggers a rollout restart of the service's deployment so the new Secret is mounted.
	// For Quadlet: writes the key file directly to the host filesystem at the encryption key directory.
	// keyFileName is the base name of the key file (e.g. "key-rotated-key").
	// keyBytes is the raw key material to store.
	SetEncryptionKey(service ServiceName, keyFileName string, keyBytes []byte) error

	// ResetEncryptionKeys removes all non-default encryption key files, leaving only the
	// original "key" file (the deployment default). Used by test recovery to undo key rotation.
	// For K8s: replaces the flightctl-encryption-key Secret's data with only the "key" entry.
	// For Quadlet: removes all key-* files from the encryption key directory.
	ResetEncryptionKeys() error

	// GetEncryptionConfig reads the current encryption block from the service's config.
	// Returns the parsed EncryptionConfig. If the service config has no encryption block
	// (e.g. Quadlet with defaults baked in), returns a synthesized default config with
	// activeKeyID=default and path=EncryptionKeyDir/key.
	// For K8s: reads ConfigMap config.yaml. For Quadlet: reads the service config file.
	GetEncryptionConfig(service ServiceName) (*internalconfig.EncryptionConfig, error)

	// SetEncryptionConfig writes the given encryption block into the service's config,
	// merging it with any existing top-level keys so unrelated settings are preserved.
	// For K8s: updates ConfigMap data["config.yaml"]. For Quadlet: writes per-service config.
	SetEncryptionConfig(service ServiceName, enc *internalconfig.EncryptionConfig) error

	// GetDBConnectionParams returns the parameters needed to connect to the flightctl database.
	// For K8s: reads hostname/port/name from the API ConfigMap and user/password from the
	// flightctl-db-app-secret Secret. For Quadlet: reads from the API service config file
	// and from secret files or environment variables.
	// Used by QueryDBExternal to reach an external DB when the built-in pod is unavailable.
	GetDBConnectionParams() (DBConnectionParams, error)

	// QueryDBExternal executes a SQL query against the flightctl database without relying on
	// the built-in flightctl-db pod. Used when BuiltinDatabaseWorkloadAvailable() is false.
	// For K8s: spawns a short-lived batch/v1 Job running psql in the cluster.
	// For Quadlet: connects directly via pgx using TCP to the database host.
	QueryDBExternal(sql string) (string, error)
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
	"flightctl-alert-exporter":      ServiceAlertExporter,
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
