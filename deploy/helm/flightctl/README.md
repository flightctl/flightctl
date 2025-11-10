# flightctl

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

A helm chart for flightctl

**Homepage:** <https://github.com/flightctl/flightctl>

## Requirements

| Repository | Name | Version |
|------------|------|---------|
| ui | ui | 0.0.1 |

## Installation

### Install Chart

```bash
# Install with default values
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl

# Install with custom values
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.yaml

# Install for development environment
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.dev.yaml

# Install for ACM (Advanced Cluster Management) integration
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.acm.yaml

# Install in specific namespace
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl --namespace flightctl --create-namespace
```

### Upgrade Chart

Flightctl uses Helm **pre-upgrade hooks** and a controlled sequence of steps to keep data consistent and minimize downtime:

1. **Scale down selected services** — services listed in `upgradeHooks.scaleDown.deployments` are **scaled to 0 in order** for a clean shutdown.
2. **Migration dry-run** — validates database migrations to catch issues early.
3. **Database migration (expand-only)** — applies backward-compatible schema changes.
4. **Service update & restart** — workloads are updated to the new spec and rolled out.

Example upgrade hooks configuration:

```yaml
upgradeHooks:
  scaleDown:
    deployments:
      - flightctl-api
      - flightctl-worker
  databaseMigrationDryRun: true  # default true
```

Note: On fresh installs, migrations run as a regular Job (not a hook).

Basic upgrade command:

```bash
helm upgrade my-flightctl oci://quay.io/flightctl/charts/flightctl
```

Upgrade to a specific chart version:

```bash
helm upgrade \
  --version <new-version> \
  my-flightctl oci://quay.io/flightctl/charts/flightctl
```

**Best Practices:**

* Consider `--atomic` so Helm waits and **automatically rolls back** if the upgrade fails.
* Before major upgrades, back up the database and configuration to ensure a clean restore point.
* **Preview the diff before upgrading** with the [Helm diff plugin](https://github.com/databus23/helm-diff).

### Rollbacks

Use rollbacks to revert to a previously successful revision if an upgrade causes issues. Use `helm history` to identify the target revision, then roll back if needed.

Show release history and see failure reasons in the DESCRIPTION column:

```bash
$ helm history my-flightctl
REVISION  UPDATED  STATUS    CHART          APP VERSION  DESCRIPTION
1         ...      deployed  flightctl-x.y.z  <appver>     Install complete
2         ...      failed    flightctl-x.y.z  <appver>     Upgrade "my-flightctl" failed: context deadline exceeded
```

Roll back to the previous successful revision (#1) and wait until it's healthy:

```bash
$ helm rollback my-flightctl 1 --wait
Rollback was a success! Happy Helming!
```

Verify that history reflects the rollback:

```bash
$ helm history my-flightctl
REVISION  UPDATED  STATUS      CHART          APP VERSION  DESCRIPTION
1         ...      superseded  flightctl-x.y.z  <appver>     Install complete
2         ...      failed      flightctl-x.y.z  <appver>     Upgrade "my-flightctl" failed: context deadline exceeded
3         ...      deployed    flightctl-x.y.z  <appver>     Rollback to 1
```

### Monitoring

Use these commands to inspect the current release state, values, and installed releases.

Show current release status and notes:

```bash
helm status my-flightctl
```

Show user-supplied values (add `--all` to include chart defaults as well):

```bash
helm get values my-flightctl
helm get values my-flightctl --all
```

List releases and observe revision bump/status after an upgrade attempt:

```bash
$ helm list
NAME        NAMESPACE  REVISION  UPDATED  STATUS    CHART           APP VERSION
my-flightctl   ...        1         ...      deployed  flightctl-x.y.z   <appver>
my-flightctl   ...        2         ...      failed    flightctl-x.y.z   <appver>
```

### Uninstall Chart

```bash
helm uninstall my-flightctl
```

## Usage

After installation, flightctl will be available in your cluster.

### Accessing Flight Control

1. **API Access**: The Flight Control API will be available at the configured endpoint
2. **UI Access**: If enabled, the web UI will be accessible through the configured route/ingress
3. **Agent Connection**: Devices can connect using the agent endpoint

### Configuration Examples

```yaml
# Example: ACM integration
global:
  target: "acm"
  auth:
    type: "k8s"
    k8s:
      externalOpenShiftApiUrl: "https://api.cluster.example.com:6443"

# Example: OpenShift standalone deployment
global:
  target: "standalone"
  baseDomain: "apps.cluster.example.com"
  auth:
    type: "k8s"
    k8s:
      externalOpenShiftApiUrl: "https://api.cluster.example.com:6443"

# Example: Kubernetes standalone deployment
global:
  target: "standalone"
  baseDomain: "flightctl.example.com"
  auth:
    type: "k8s"
    k8s:
      apiUrl: "https://kubernetes.default.svc"
```

### TLS/SSL Certificate Configuration

When using external PostgreSQL databases with TLS/SSL, Flight Control supports multiple certificate management options:

#### Option 1: Kubernetes ConfigMap/Secret (Production)

```bash
# Create certificate resources
kubectl create configmap postgres-ca-cert \
  --from-file=ca-cert.pem=/path/to/ca-cert.pem

kubectl create secret generic postgres-client-certs \
  --from-file=client-cert.pem=/path/to/client-cert.pem \
  --from-file=client-key.pem=/path/to/client-key.pem
```

```yaml
# Configure in values.yaml
db:
  external: "enabled"
  hostname: "postgres.example.com"
  sslmode: "verify-ca"
  sslConfigMap: "postgres-ca-cert"     # ConfigMap containing CA certificate
  sslSecret: "postgres-client-certs"   # Secret containing client certificates
```

**TLS/SSL Modes:**
- `disable` - No TLS/SSL (not recommended for production)
- `allow` - TLS/SSL if available, otherwise plain connection
- `prefer` - TLS/SSL preferred, fallback to plain connection
- `require` - TLS/SSL required, no certificate verification
- `verify-ca` - TLS/SSL required, verify server certificate against CA
- `verify-full` - TLS/SSL required, verify certificate and hostname

For complete TLS/SSL configuration details, see the [external database documentation](../../../docs/user/external-database.md).

For more detailed configuration options, see the [Values](#values) section below.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| alertExporter | object | `{"enabled":true,"image":{"image":"quay.io/flightctl/flightctl-alert-exporter","pullPolicy":"","tag":""}}` | Alert Exporter Configuration |
| alertExporter.enabled | bool | `true` | Enable alert exporter service |
| alertExporter.image.image | string | `"quay.io/flightctl/flightctl-alert-exporter"` | Alert exporter container image |
| alertExporter.image.pullPolicy | string | `""` | Image pull policy for alert exporter container |
| alertExporter.image.tag | string | `""` | Alert exporter image tag |
| alertmanager | object | `{"enabled":true,"image":{"image":"quay.io/prometheus/alertmanager","pullPolicy":"","tag":"v0.28.1"}}` | Alertmanager Configuration |
| alertmanager.enabled | bool | `true` | Enable Alertmanager for alert handling |
| alertmanager.image.image | string | `"quay.io/prometheus/alertmanager"` | Alertmanager container image |
| alertmanager.image.pullPolicy | string | `""` | Image pull policy for Alertmanager container |
| alertmanager.image.tag | string | `"v0.28.1"` | Alertmanager image tag |
| alertmanagerProxy | object | `{"enabled":true,"image":{"image":"quay.io/flightctl/flightctl-alertmanager-proxy","pullPolicy":"","tag":""}}` | Alertmanager Proxy Configuration |
| alertmanagerProxy.enabled | bool | `true` | Enable Alertmanager proxy service |
| alertmanagerProxy.image.image | string | `"quay.io/flightctl/flightctl-alertmanager-proxy"` | Alertmanager proxy container image |
| alertmanagerProxy.image.pullPolicy | string | `""` | Image pull policy for Alertmanager proxy container |
| alertmanagerProxy.image.tag | string | `""` | Alertmanager proxy image tag |
| api | object | `{"baseUIUrl":"","enabled":true,"image":{"image":"quay.io/flightctl/flightctl-api","pullPolicy":"","tag":""},"probes":{"enabled":true,"livenessPath":"/healthz","readinessPath":"/readyz"},"rateLimit":{"authRequests":20,"authWindow":"1h","enabled":true,"requests":300,"trustedProxies":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"],"window":"1m"}}` | API Server Configuration |
| api.baseUIUrl | string | `""` | Base URL for the web UI (used for CORS and redirects) |
| api.enabled | bool | `true` | Enable Flight Control API server deployment |
| api.image.image | string | `"quay.io/flightctl/flightctl-api"` | API server container image |
| api.image.pullPolicy | string | `""` | Image pull policy for API server container |
| api.image.tag | string | `""` | API server image tag (leave empty to use chart appVersion) |
| api.probes.enabled | bool | `true` | Enable health and readiness probes for API server |
| api.probes.livenessPath | string | `"/healthz"` | HTTP path for liveness probe |
| api.probes.readinessPath | string | `"/readyz"` | HTTP path for readiness probe |
| api.rateLimit.authRequests | int | `20` | Maximum authentication requests per auth window Auth-specific rate limiting |
| api.rateLimit.authWindow | string | `"1h"` | Time window for authentication rate limiting |
| api.rateLimit.enabled | bool | `true` | Enable or disable rate limiting |
| api.rateLimit.requests | int | `300` | Maximum requests per window for general API endpoints General API rate limiting |
| api.rateLimit.trustedProxies | list | `["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"]` | List of trusted proxy IP ranges that can set X-Forwarded-For headers Trusted proxies that can set X-Forwarded-For/X-Real-IP headers This should include your load balancer and UI proxy IPs |
| api.rateLimit.window | string | `"1m"` | Time window for rate limiting (e.g., "1m", "1h") |
| cliArtifacts | object | `{"enabled":true,"image":{"image":"quay.io/flightctl/flightctl-cli-artifacts","pullPolicy":"","tag":""}}` | CLI Artifacts Configuration |
| cliArtifacts.enabled | bool | `true` | Enable CLI artifacts service |
| cliArtifacts.image.image | string | `"quay.io/flightctl/flightctl-cli-artifacts"` | CLI artifacts container image |
| cliArtifacts.image.pullPolicy | string | `""` | Image pull policy for CLI artifacts container |
| cliArtifacts.image.tag | string | `""` | CLI artifacts image tag |
| clusterCli | object | `{"image":{"image":"quay.io/openshift/origin-cli","pullPolicy":"","tag":"4.20.0"}}` | Cluster CLI Configuration |
| clusterCli.image.image | string | `"quay.io/openshift/origin-cli"` | Cluster CLI container image |
| clusterCli.image.pullPolicy | string | `""` | Image pull policy for cluster CLI container |
| clusterCli.image.tag | string | `"4.20.0"` | Cluster CLI image tag |
| db | object | `{"external":"disabled","fsGroup":"","image":{"image":"quay.io/sclorg/postgresql-16-c9s","pullPolicy":"","tag":"20250214"},"masterPassword":"","masterUser":"admin","maxConnections":200,"migrationPassword":"","migrationUser":"flightctl_migrator","name":"flightctl","port":5432,"resources":{"requests":{"cpu":"512m","memory":"512Mi"}},"sslConfigMap":"","sslSecret":"","sslmode":"","storage":{"size":"60Gi"},"type":"pgsql","user":"flightctl_app","userPassword":""}` | Database Configuration |
| db.external | string | `"disabled"` | Use external PostgreSQL database instead of deploying internal one external: Set to "enabled" to use external PostgreSQL database instead of deploying internal one When enabled, configure hostname, port, name, user credentials to point to your external database |
| db.fsGroup | string | `""` | File system group ID for database pod security context |
| db.image.image | string | `"quay.io/sclorg/postgresql-16-c9s"` | PostgreSQL container image |
| db.image.pullPolicy | string | `""` | Image pull policy for database container |
| db.image.tag | string | `"20250214"` | PostgreSQL image tag |
| db.masterPassword | string | `""` | Master user password (leave empty for auto-generation) masterPassword: Leave empty to auto-generate secure password, or set to use a specific password. |
| db.masterUser | string | `"admin"` | Database master/admin username |
| db.maxConnections | int | `200` | Maximum number of database connections |
| db.migrationPassword | string | `""` | Migration user password (leave empty for auto-generation) migrationPassword: Leave empty to auto-generate secure password, or set to use a specific password. |
| db.migrationUser | string | `"flightctl_migrator"` | Database migration username |
| db.name | string | `"flightctl"` | Database name for Flight Control |
| db.port | int | `5432` | Database port number |
| db.resources.requests.cpu | string | `"512m"` | CPU resource requests for database pod |
| db.resources.requests.memory | string | `"512Mi"` | Memory resource requests for database pod |
| db.sslConfigMap | string | `""` | ConfigMap containing CA certificate (automatically mounted at /etc/ssl/postgres/) |
| db.sslSecret | string | `""` | Secret containing client certificates (automatically mounted at /etc/ssl/postgres/) |
| db.sslmode | string | `""` | SSL mode for database connections (disable, allow, prefer, require, verify-ca, verify-full) |
| db.storage.size | string | `"60Gi"` | Persistent volume size for database storage |
| db.type | string | `"pgsql"` | Database type (currently only 'pgsql' is supported) |
| db.user | string | `"flightctl_app"` | Application database username |
| db.userPassword | string | `""` | Application user password (leave empty for auto-generation) userPassword: Leave empty to auto-generate secure password, or set to use a specific password. |
| dbSetup | object | `{"image":{"image":"quay.io/flightctl/flightctl-db-setup","pullPolicy":"","tag":""},"migration":{"activeDeadlineSeconds":0,"backoffLimit":2147483647},"wait":{"sleep":2,"timeout":60}}` | Database Setup Configuration |
| dbSetup.image.image | string | `"quay.io/flightctl/flightctl-db-setup"` | Database setup container image |
| dbSetup.image.pullPolicy | string | `""` | Image pull policy for database setup container |
| dbSetup.image.tag | string | `""` | Database setup image tag |
| dbSetup.migration.activeDeadlineSeconds | int | `0` | Maximum runtime in seconds for the migration Job (0 = no deadline) |
| dbSetup.migration.backoffLimit | int | `2147483647` | Number of retries for the migration Job on failure  |
| dbSetup.wait.sleep | int | `2` | Seconds to sleep between database connection attempts Default sleep interval between connection attempts |
| dbSetup.wait.timeout | int | `60` | Seconds to wait for database readiness before failing Default timeout for database wait (can be overridden per deployment) |
| global.apiUrl | string | `""` | Alternative to global.auth.k8s.externalOpenShiftApiUrl with the same meaning, used by the multiclusterhub operator |
| global.appCode | string | `""` | This is only related to deployment in Red Hat's PAAS. |
| global.auth.aap.apiUrl | string | `""` | The URL of the AAP Gateway API endpoint |
| global.auth.aap.externalApiUrl | string | `""` | The URL of the AAP Gateway API endpoint that is reachable by clients |
| global.auth.caCert | string | `""` | The custom CA cert. |
| global.auth.insecureSkipTlsVerify | bool | `false` | True if verification of authority TLS cert should be skipped. |
| global.auth.k8s.apiUrl | string | `"https://kubernetes.default.svc"` | API URL of k8s cluster that will be used as authentication authority |
| global.auth.k8s.externalApiToken | string | `""` | In case flightctl is not running within a cluster, you can provide api token |
| global.auth.k8s.externalOpenShiftApiUrl | string | `""` | API URL of OpenShift cluster that can be accessed by external client to retrieve auth token |
| global.auth.k8s.rbacNs | string | `""` | Namespace that should be used for the RBAC checks |
| global.auth.oidc.clientId | string | `"flightctl-client"` | OIDC Client ID |
| global.auth.oidc.enabled | bool | `true` | Whether this OIDC provider is enabled |
| global.auth.oidc.externalOidcAuthority | string | `""` | The base URL for the OIDC provider that is reachable by clients. Example: https://auth.foo.net/realms/flightctl |
| global.auth.oidc.issuer | string | `""` | The base URL for the OIDC provider that is reachable by flightctl services. Example: https://auth.foo.internal/realms/flightctl |
| global.auth.type | string | `"oidc"` | Type of the auth to use. Can be one of 'k8s', 'oidc', 'builtin', 'aap', or 'none' Note: 'builtin' is a legacy mode that translates to 'oidc' with PAM issuer automatically enabled For new deployments, explicitly set type to 'oidc' and configure pamOidcIssuer settings |
| global.baseDomain | string | `""` | Base domain to construct the FQDN for the service endpoints. |
| global.baseDomainTls.cert | string | `""` | Certificate for the base domain wildcard certificate, it should be valid for *.${baseDomain}. This certificate is only used for non mTLS endpoints, mTLS endpoints like agent-api, etc will use different certificates. |
| global.baseDomainTls.key | string | `""` | Key for the base domain wildcard certificate. |
| global.clusterLevelSecretAccess | bool | `false` | Allow flightctl-worker to access secrets at the cluster level for embedding in device configs |
| global.exposeServicesMethod | string | `"route"` | How the Flight Control services should be exposed. Can be either nodePort or route |
| global.gatewayClass | string | `""` | Gateway API class name for gateway exposure method |
| global.gatewayPorts.http | int | `80` | HTTP port for Gateway API configuration |
| global.gatewayPorts.tls | int | `443` | TLS port for Gateway API configuration |
| global.generateSecrets | bool | `true` | Generate secrets when deploying Flight Control. This should be set to false if you want to provide your own secrets or when upgrading Flight Control to avoid overriding the existing secrets |
| global.imagePullPolicy | string | `"IfNotPresent"` | Image pull policy for all containers |
| global.imagePullSecretName | string | `""` | Name of the image pull secret for accessing private container registries |
| global.internalNamespace | string | `""` | Namespace where internal components are deployed |
| global.metrics.enabled | bool | `true` | Enable metrics exporting and service |
| global.nodePorts.agent | int | `7443` | NodePort for agent communication service |
| global.nodePorts.alertmanagerProxy | int | `8443` | NodePort for Alertmanager proxy service |
| global.nodePorts.api | int | `3443` | NodePort for Flight Control API service |
| global.nodePorts.cliArtifacts | int | `8090` | NodePort for CLI artifacts service |
| global.nodePorts.telemetryGatewayOtlp | int | `4317` | NodePort for OTLP telemetry gateway |
| global.nodePorts.telemetryGatewayProm | int | `9464` | NodePort for Prometheus telemetry gateway |
| global.nodePorts.ui | int | `9000` | NodePort for web UI service |
| global.organizations.enabled | bool | `false` | Enable IDP-provided organizations support |
| global.rbac.create | bool | `true` | Create RBAC resources (roles, bindings, service accounts) |
| global.sshKnownHosts.data | string | `""` | SSH known hosts file content for Git repository host key verification. |
| global.target | string | `"standalone"` | The type of Flightctl to deploy - either 'standalone' or 'acm'. |
| global.tracing.enabled | bool | `false` | Enable distributed tracing with OpenTelemetry |
| global.tracing.endpoint | string | `"jaeger-collector.flightctl-e2e.svc.cluster.local:4318"` | OpenTelemetry collector endpoint for trace data |
| global.tracing.insecure | bool | `true` | Use insecure connection to tracing endpoint (development only) |
| kv | object | `{"enabled":true,"fsGroup":"","image":{"image":"quay.io/sclorg/redis-7-c9s","pullPolicy":"","tag":"20250108"},"loglevel":"warning","maxmemory":"1gb","maxmemoryPolicy":"allkeys-lru","password":""}` | Key-Value Store Configuration |
| kv.enabled | bool | `true` | Enable Redis key-value store for caching and session storage |
| kv.fsGroup | string | `""` | File system group ID for Redis pod security context |
| kv.image.image | string | `"quay.io/sclorg/redis-7-c9s"` | Redis container image |
| kv.image.pullPolicy | string | `""` | Image pull policy for Redis container |
| kv.image.tag | string | `"20250108"` | Redis image tag |
| kv.loglevel | string | `"warning"` | Redis log level (debug, verbose, notice, warning) |
| kv.maxmemory | string | `"1gb"` | Maximum memory usage for Redis |
| kv.maxmemoryPolicy | string | `"allkeys-lru"` | Redis memory eviction policy |
| kv.password | string | `""` | Redis password (leave empty for auto-generation) password: Leave empty to auto-generate secure password, or set to use a specific password. |
| periodic | object | `{"consumers":5,"enabled":true,"image":{"image":"quay.io/flightctl/flightctl-periodic","pullPolicy":"","tag":""}}` | Periodic Configuration |
| periodic.consumers | int | `5` | Number of periodic consumers |
| periodic.enabled | bool | `true` | Enable Flight Control periodic service |
| periodic.image.image | string | `"quay.io/flightctl/flightctl-periodic"` | Periodic container image |
| periodic.image.pullPolicy | string | `""` | Image pull policy for periodic container |
| periodic.image.tag | string | `""` | Periodic image tag |
| prometheus | object | `{"enabled":false}` | Prometheus Configuration |
| prometheus.enabled | bool | `false` | Enable Prometheus deployment |
| telemetryGateway | object | `{"enabled":false}` | Telemetry Gateway Configuration |
| telemetryGateway.enabled | bool | `false` | Enable telemetry gateway service |
| ui | object | `{"api":{"insecureSkipTlsVerify":true},"enabled":true}` | UI Configuration |
| ui.api.insecureSkipTlsVerify | bool | `true` | Skip TLS verification for UI API calls |
| ui.enabled | bool | `true` | Enable web UI deployment |
| upgradeHooks | object | `{"databaseMigrationDryRun":true,"scaleDown":{"condition":"chart","deployments":["flightctl-periodic","flightctl-worker"],"timeoutSeconds":120}}` | Upgrade hooks |
| upgradeHooks.databaseMigrationDryRun | bool | `true` | Enable pre-upgrade DB migration dry-run as a hook |
| upgradeHooks.scaleDown.condition | string | `"chart"` | When to run pre-upgrade scale down job: "always", "never", or "chart" (default). "chart" runs only if helm.sh/chart changed. |
| upgradeHooks.scaleDown.deployments | list | `["flightctl-periodic","flightctl-worker"]` | List of Deployments to scale down in order |
| upgradeHooks.scaleDown.timeoutSeconds | int | `120` | Timeout in seconds to wait for rollout per Deployment |
| worker | object | `{"enableSecretsClusterRoleBinding":true,"enabled":true,"image":{"image":"quay.io/flightctl/flightctl-worker","pullPolicy":"","tag":""}}` | Worker Configuration |
| worker.enableSecretsClusterRoleBinding | bool | `true` | Enable secrets cluster role binding for worker |
| worker.enabled | bool | `true` | Enable Flight Control worker deployment |
| worker.image.image | string | `"quay.io/flightctl/flightctl-worker"` | Worker container image |
| worker.image.pullPolicy | string | `""` | Image pull policy for worker container |
| worker.image.tag | string | `""` | Worker image tag |

## Environment-Specific Values Files

This chart includes additional values files for different deployment scenarios:

### `values.dev.yaml`

Development environment configuration with:

- Local development settings
- Debug configurations
- Development-specific image tags

### `values.acm.yaml`

Advanced Cluster Management (ACM) integration configuration with:

- ACM-specific authentication settings
- Red Hat registry images
- ACM integration parameters

To use these files:

```bash
# Development deployment
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.dev.yaml

# ACM integration deployment
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.acm.yaml

# Combine multiple values files (later files override earlier ones)
helm install my-flightctl oci://quay.io/flightctl/charts/flightctl -f values.yaml -f values.acm.yaml -f my-custom.yaml
```

## Contributing

Please read the [contributing guidelines](https://github.com/flightctl/flightctl/blob/main/CONTRIBUTING.md) for details on our code of conduct and the process for submitting pull requests.

## License

This project is licensed under the Apache License 2.0 - see the [LICENSE](https://github.com/flightctl/flightctl/blob/main/LICENSE) file for details.
