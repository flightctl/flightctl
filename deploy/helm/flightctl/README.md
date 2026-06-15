# redhat-rhem

![Version: 0.1.0](https://img.shields.io/badge/Version-0.1.0-informational?style=flat-square) ![Type: application](https://img.shields.io/badge/Type-application-informational?style=flat-square) ![AppVersion: latest](https://img.shields.io/badge/AppVersion-latest-informational?style=flat-square)

A helm chart for Red Hat Edge Manager

**Homepage:** <https://red.ht/rhem>

## Prerequisites

### Pod Security Standards

The `flightctl-imagebuilder-worker` component requires elevated privileges to build OS images using osbuild/bootc. It needs:

- Privileged container access
- Host device access (`/dev`, `/sys`, `/lib/modules`)
- Capabilities: `SYS_ADMIN`, `MKNOD`, `SYS_CHROOT`, `SETFCAP`

If your namespace enforces the "restricted" Pod Security Standard, you will see warnings during deployment. To suppress these warnings, configure the namespace labels:

```bash
kubectl label namespace <namespace> \
  pod-security.kubernetes.io/enforce=privileged \
  pod-security.kubernetes.io/warn=privileged \
  pod-security.kubernetes.io/audit=privileged
```

On OpenShift clusters, the chart automatically creates a SecurityContextConstraints (SCC) that grants the required permissions to the `flightctl-imagebuilder-worker` service account.

For additional ImageBuilder Worker configuration options (custom RPM repositories, RHEL entitlement certificates), see the [ImageBuilder configuration documentation](../../../docs/user/installing/configuring-imagebuilder.md).

SBOM defaults (see `imageBuilderWorker.sbom` and `imageBuilderWorker.serviceImages.syft` in the parameters table): when `serviceImages.syft.image` is empty, the worker uses `docker.io/anchore/syft:v1.44.0`. Podman and bootc-image-builder use their own defaults when those image fields are empty.

If you don't need image building capabilities, you can disable the imagebuilder-worker:

```yaml
imageBuilderWorker:
  enabled: false
```

## Installation

### Install Chart

```bash
# Install with default values
helm install my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem

# Install with custom values
helm install my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem -f values.yaml

# Install for development environment
helm install my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem -f values.dev.yaml

# Install for ACM (Advanced Cluster Management) integration
helm install my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem -f values.acm.yaml

# Install in specific namespace
helm install my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem --namespace flightctl --create-namespace
```

### Upgrade Chart

Redhat-Rhem uses Helm **pre-upgrade hooks** and a controlled sequence of steps to keep data consistent and minimize downtime:

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
helm upgrade my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem
```

Upgrade to a specific chart version:

```bash
helm upgrade \
  --version <new-version> \
  my-redhat-rhem oci://quay.io/flightctl/charts/redhat-rhem
```

**Best Practices:**

* Consider `--atomic` so Helm waits and **automatically rolls back** if the upgrade fails.
* Before major upgrades, back up the database and configuration to ensure a clean restore point.
* **Preview the diff before upgrading** with the [Helm diff plugin](https://github.com/databus23/helm-diff).

**Upgrade Notes (1.0 → 1.1):**

* When the UI is deployed as a plugin to multicluster engine, the TLS secret was renamed from `flightctl-ui-serving-cert` to `flightctl-ui-server-tls`. After upgrading, the old secret is orphaned. Once the UI pod is running successfully, you can safely delete it with `kubectl delete secret flightctl-ui-serving-cert -n <namespace>`.

### Rollbacks

Use rollbacks to revert to a previously successful revision if an upgrade causes issues. Use `helm history` to identify the target revision, then roll back if needed.

Show release history and see failure reasons in the DESCRIPTION column:

```bash
$ helm history my-redhat-rhem
REVISION  UPDATED  STATUS    CHART          APP VERSION  DESCRIPTION
1         ...      deployed  redhat-rhem-x.y.z  <appver>     Install complete
2         ...      failed    redhat-rhem-x.y.z  <appver>     Upgrade "my-redhat-rhem" failed: context deadline exceeded
```

Roll back to the previous successful revision (#1) and wait until it's healthy:

```bash
$ helm rollback my-redhat-rhem 1 --wait
Rollback was a success! Happy Helming!
```

Verify that history reflects the rollback:

```bash
$ helm history my-redhat-rhem
REVISION  UPDATED  STATUS      CHART          APP VERSION  DESCRIPTION
1         ...      superseded  redhat-rhem-x.y.z  <appver>     Install complete
2         ...      failed      redhat-rhem-x.y.z  <appver>     Upgrade "my-redhat-rhem" failed: context deadline exceeded
3         ...      deployed    redhat-rhem-x.y.z  <appver>     Rollback to 1
```

### Monitoring

Use these commands to inspect the current release state, values, and installed releases.

Show current release status and notes:

```bash
helm status my-redhat-rhem
```

Show user-supplied values (add `--all` to include chart defaults as well):

```bash
helm get values my-redhat-rhem
helm get values my-redhat-rhem --all
```

List releases and observe revision bump/status after an upgrade attempt:

```bash
$ helm list
NAME        NAMESPACE  REVISION  UPDATED  STATUS    CHART           APP VERSION
my-redhat-rhem   ...        1         ...      deployed  redhat-rhem-x.y.z   <appver>
my-redhat-rhem   ...        2         ...      failed    redhat-rhem-x.y.z   <appver>
```

### Uninstall Chart

```bash
helm uninstall my-redhat-rhem
```

## Usage

After installation, redhat-rhem will be available in your cluster.

For more detailed configuration options, see the [Values](#values) section below.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| alertExporter | object | `{"enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-alert-exporter-rhel9","pullPolicy":"","tag":""}}` | Alert Exporter Configuration |
| alertExporter.enabled | bool | `true` | Enable alert exporter service |
| alertExporter.image.image | string | `"registry.redhat.io/rhem/flightctl-alert-exporter-rhel9"` | Alert exporter container image |
| alertExporter.image.pullPolicy | string | `""` | Image pull policy for alert exporter container |
| alertExporter.image.tag | string | `""` | Alert exporter image tag |
| alertmanager | object | `{"additionalPVCLabels":null,"additionalRouteLabels":null,"enabled":true,"image":{"image":"registry.redhat.io/rhacm2/prometheus-alertmanager-rhel9","pullPolicy":"","tag":"v2.15.0-1"}}` | Alertmanager Configuration |
| alertmanager.additionalPVCLabels | string | `nil` | Additional labels for Alert Manager PVCs. |
| alertmanager.additionalRouteLabels | string | `nil` | Additional labels for Alert Manager routes. |
| alertmanager.enabled | bool | `true` | Enable Alertmanager for alert handling |
| alertmanager.image.image | string | `"registry.redhat.io/rhacm2/prometheus-alertmanager-rhel9"` | Alertmanager container image |
| alertmanager.image.pullPolicy | string | `""` | Image pull policy for Alertmanager container |
| alertmanager.image.tag | string | `"v2.15.0-1"` | Alertmanager image tag |
| alertmanagerProxy | object | `{"enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-alertmanager-proxy-rhel9","pullPolicy":"","tag":""}}` | Alertmanager Proxy Configuration |
| alertmanagerProxy.enabled | bool | `true` | Enable Alertmanager proxy service |
| alertmanagerProxy.image.image | string | `"registry.redhat.io/rhem/flightctl-alertmanager-proxy-rhel9"` | Alertmanager proxy container image |
| alertmanagerProxy.image.pullPolicy | string | `""` | Image pull policy for Alertmanager proxy container |
| alertmanagerProxy.image.tag | string | `""` | Alertmanager proxy image tag |
| api | object | `{"additionalPVCLabels":null,"additionalRouteLabels":null,"image":{"image":"registry.redhat.io/rhem/flightctl-api-rhel9","pullPolicy":"","tag":""},"rateLimit":{"authRequests":20,"authWindow":"1h","enabled":true,"requests":300,"trustedProxies":["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"],"window":"1m"}}` | API Server Configuration |
| api.additionalPVCLabels | string | `nil` | Additional labels for API PVCs. |
| api.additionalRouteLabels | string | `nil` | Additional labels for API routes. |
| api.image.image | string | `"registry.redhat.io/rhem/flightctl-api-rhel9"` | API server container image |
| api.image.pullPolicy | string | `""` | Image pull policy for API server container |
| api.image.tag | string | `""` | API server image tag (leave empty to use chart appVersion) |
| api.rateLimit.authRequests | int | `20` | Maximum authentication requests per auth window Auth-specific rate limiting |
| api.rateLimit.authWindow | string | `"1h"` | Time window for authentication rate limiting |
| api.rateLimit.enabled | bool | `true` | Enable or disable rate limiting |
| api.rateLimit.requests | int | `300` | Maximum requests per window for general API endpoints General API rate limiting |
| api.rateLimit.trustedProxies | list | `["10.0.0.0/8","172.16.0.0/12","192.168.0.0/16"]` | List of trusted proxy IP ranges that can set X-Forwarded-For headers Trusted proxies that can set X-Forwarded-For/X-Real-IP headers This should include your load balancer and UI proxy IPs |
| api.rateLimit.window | string | `"1m"` | Time window for rate limiting (e.g., "1m", "1h") |
| cliArtifacts | object | `{"additionalRouteLabels":null,"enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-cli-artifacts-rhel9","pullPolicy":"","tag":""}}` | CLI Artifacts Configuration |
| cliArtifacts.additionalRouteLabels | string | `nil` | Additional labels for CLI Artifacts routes. |
| cliArtifacts.enabled | bool | `true` | Enable CLI artifacts service |
| cliArtifacts.image.image | string | `"registry.redhat.io/rhem/flightctl-cli-artifacts-rhel9"` | CLI artifacts container image |
| cliArtifacts.image.pullPolicy | string | `""` | Image pull policy for CLI artifacts container |
| cliArtifacts.image.tag | string | `""` | CLI artifacts image tag |
| clusterCli | object | `{"image":{"image":"registry.redhat.io/openshift4/ose-cli-rhel9","pullPolicy":"","tag":"v4.20.0"}}` | Cluster CLI Configuration |
| clusterCli.image.image | string | `"registry.redhat.io/openshift4/ose-cli-rhel9"` | Cluster CLI container image |
| clusterCli.image.pullPolicy | string | `""` | Image pull policy for cluster CLI container |
| clusterCli.image.tag | string | `"v4.20.0"` | Cluster CLI image tag |
| db | object | `{"builtin":{"additionalPVCLabels":null,"applicationUserSecretName":"","fsGroup":"","image":{"image":"registry.redhat.io/rhel9/postgresql-16","pullPolicy":"","tag":"9.7-1766414426"},"masterUserSecretName":"","maxConnections":200,"migrationUserSecretName":"","resources":{"requests":{"cpu":"512m","memory":"512Mi"}},"storage":{"size":"60Gi"}},"external":{"applicationUserSecretName":"","hostname":"","migrationUserSecretName":"","port":5432,"sslmode":"","tlsConfigMapName":"","tlsSecretName":""},"name":"flightctl","type":"builtin"}` | Database Configuration |
| db.builtin | object | `{"additionalPVCLabels":null,"applicationUserSecretName":"","fsGroup":"","image":{"image":"registry.redhat.io/rhel9/postgresql-16","pullPolicy":"","tag":"9.7-1766414426"},"masterUserSecretName":"","maxConnections":200,"migrationUserSecretName":"","resources":{"requests":{"cpu":"512m","memory":"512Mi"}},"storage":{"size":"60Gi"}}` | Settings for builtin DB |
| db.builtin.additionalPVCLabels | string | `nil` | Additional labels for DB PVCs. |
| db.builtin.applicationUserSecretName | string | `""` | Database application user secret name containing username/password. If not provided, the secret will be generated |
| db.builtin.fsGroup | string | `""` | File system group ID for database pod security context |
| db.builtin.image.image | string | `"registry.redhat.io/rhel9/postgresql-16"` | PostgreSQL container image |
| db.builtin.image.pullPolicy | string | `""` | Image pull policy for database container |
| db.builtin.image.tag | string | `"9.7-1766414426"` | PostgreSQL image tag |
| db.builtin.masterUserSecretName | string | `""` | Database master/admin secret name containing username/password. If not provided, the secret will be generated |
| db.builtin.maxConnections | int | `200` | Maximum number of database connections |
| db.builtin.migrationUserSecretName | string | `""` | Database migration user secret name containing username/password. If not provided, the secret will be generated |
| db.builtin.resources.requests.cpu | string | `"512m"` | CPU resource requests for database pod |
| db.builtin.resources.requests.memory | string | `"512Mi"` | Memory resource requests for database pod |
| db.builtin.storage.size | string | `"60Gi"` | Persistent volume size for database storage |
| db.external.applicationUserSecretName | string | `""` | Database application user secret name containing username/password. |
| db.external.hostname | string | `""` | External database hostname |
| db.external.migrationUserSecretName | string | `""` | Database migration user secret name containing username/password. |
| db.external.port | int | `5432` | Database port number |
| db.external.sslmode | string | `""` | SSL mode for database connections (disable, allow, prefer, require, verify-ca, verify-full) |
| db.external.tlsConfigMapName | string | `""` | ConfigMap containing CA certificate (automatically mounted at /etc/ssl/postgres/) |
| db.external.tlsSecretName | string | `""` | Secret containing client certificates (automatically mounted at /etc/ssl/postgres/) |
| db.name | string | `"flightctl"` | Database name for Flight Control |
| db.type | string | `"builtin"` | Type of database to use. Can be 'builtin' or 'external'. Only PostgreSQL DB is supported. |
| dbSetup | object | `{"image":{"image":"registry.redhat.io/rhem/flightctl-db-setup-rhel9","pullPolicy":"","tag":""},"migration":{"activeDeadlineSeconds":0,"backoffLimit":2147483647},"wait":{"sleep":2,"timeout":60}}` | Database Setup Configuration |
| dbSetup.image.image | string | `"registry.redhat.io/rhem/flightctl-db-setup-rhel9"` | Database setup container image |
| dbSetup.image.pullPolicy | string | `""` | Image pull policy for database setup container |
| dbSetup.image.tag | string | `""` | Database setup image tag |
| dbSetup.migration.activeDeadlineSeconds | int | `0` | Maximum runtime in seconds for the migration Job (0 = no deadline) |
| dbSetup.migration.backoffLimit | int | `2147483647` | Number of retries for the migration Job on failure  |
| dbSetup.wait.sleep | int | `2` | Seconds to sleep between database connection attempts Default sleep interval between connection attempts |
| dbSetup.wait.timeout | int | `60` | Seconds to wait for database readiness before failing Default timeout for database wait (can be overridden per deployment) |
| global.additionalPVCLabels | string | `nil` | Additional labels for PVCs. |
| global.additionalRouteLabels | string | `nil` | Additional labels for routes. |
| global.auth.aap.apiUrl | string | `""` | The URL of the AAP Gateway API endpoint |
| global.auth.aap.authorizationUrl | string | `""` | OAuth2 authorization endpoint URL |
| global.auth.aap.clientId | string | `""` | OAuth2 client ID |
| global.auth.aap.clientSecret | string | `""` | OAuth2 client secret (prefer mounting from a secret) |
| global.auth.aap.enabled | bool | `true` | Whether the AAP provider is enabled |
| global.auth.aap.externalApiUrl | string | `""` | The URL of the AAP Gateway API endpoint that is reachable by clients |
| global.auth.aap.organizationNamePrefix | string | `""` | Optional prefix for org names from this provider (e.g. "aap-"). Incoming org names are exposed as prefix + name. |
| global.auth.aap.scopes | list | `["read","write"]` | List of OAuth2 scopes to request |
| global.auth.aap.tokenUrl | string | `""` | OAuth2 token endpoint URL |
| global.auth.caCert | string | `""` | The custom CA cert. |
| global.auth.insecureSkipTlsVerify | bool | `false` | True if verification of authority TLS cert should be skipped. |
| global.auth.k8s.apiUrl | string | `"https://kubernetes.default.svc"` | API URL of k8s cluster that will be used as authentication authority |
| global.auth.k8s.createAdminUser | bool | `true` | Create default flightctl-admin ServiceAccount with admin access |
| global.auth.k8s.externalApiTokenSecretName | string | `""` | In case flightctl is not running within a cluster, you can provide a name of a secret that holds the API token |
| global.auth.k8s.organizationNamePrefix | string | `""` | Optional prefix for org names from this provider (e.g. "k8s-"). Incoming org name is exposed as prefix + name. |
| global.auth.k8s.rbacNs | string | `""` | Namespace that should be used for the RBAC checks |
| global.auth.oidc.clientId | string | `"flightctl-client"` | OIDC Client ID |
| global.auth.oidc.clientSecret | string | `""` | OIDC client secret (optional; prefer mounting from a secret) |
| global.auth.oidc.externalOidcAuthority | string | `""` | The base URL for the OIDC provider that is reachable by clients. Example: https://auth.foo.net/realms/flightctl |
| global.auth.oidc.issuer | string | `""` | The base URL for the OIDC provider that is reachable by flightctl services. Example: https://auth.foo.internal/realms/flightctl |
| global.auth.oidc.organizationAssignment | object | `{"organizationName":"default","type":"static"}` | Organization assignment configuration |
| global.auth.oidc.roleAssignment | object | `{"claimPath":["groups"],"type":"dynamic"}` | Role assignment configuration |
| global.auth.oidc.scopes | list | `["openid","profile","email","roles","offline_access"]` | List of OIDC scopes to request (e.g. openid, profile, email, roles, offline_access) |
| global.auth.oidc.usernameClaim | list | `["preferred_username"]` | Username claim to extract from OIDC token (default: "preferred_username") |
| global.auth.openshift.authorizationUrl | string | `""` | OAuth authorization URL (leave empty to auto-detect from OpenShift cluster) |
| global.auth.openshift.clientId | string | `""` | OAuth client ID (will be set to flightctl-{releaseName}) |
| global.auth.openshift.clientSecret | string | `""` | OAuth client secret (leave empty for auto-generation) |
| global.auth.openshift.clusterControlPlaneUrl | string | `"https://kubernetes.default.svc"` | OpenShift cluster control plane API URL for RBAC checks (leave empty for auto-detection) |
| global.auth.openshift.createAdminUser | bool | `true` | Create default flightctl-admin ServiceAccount with admin access |
| global.auth.openshift.externalApiTokenSecretName | string | `""` | In case flightctl is not running within a cluster, you can provide a name of a secret that holds the API token |
| global.auth.openshift.issuer | string | `""` | OAuth issuer URL (defaults to authorizationUrl if not specified) |
| global.auth.openshift.organizationNamePrefix | string | `""` | Optional prefix for org (project) names from this provider (e.g. "ocp-"). Incoming names are exposed as prefix + name. |
| global.auth.openshift.projectLabelFilter | string | `""` | Project label filter for OpenShift projects (leave empty to use default: io.flightctl/instance=<releaseName>) |
| global.auth.openshift.tokenUrl | string | `""` | OAuth token URL (leave empty to auto-detect from OpenShift cluster) |
| global.auth.type | string | `""` | Type of authentication to use. Allowed values: 'k8s', 'oidc', 'aap', 'openshift', 'oauth2', or 'none'. When left empty (default and recommended), authentication type is auto-detected: 'openshift' on OpenShift clusters, 'k8s' otherwise. |
| global.baseDomain | string | `""` | Base domain to construct the FQDN for the service endpoints. |
| global.baseDomainTlsSecretName | string | `""` | Secret containing TLS ca/cert/key. It should be valid for *.${baseDomain}. This certificate is only used for non-mTLS endpoints, mTLS endpoints like agent-api, etc will use different certificates. |
| global.enableMulticlusterExtensions | string | `"auto"` | Enable MultiCluster Engine extensions - one of 'auto', 'true', 'false'. |
| global.enableOpenShiftExtensions | string | `"auto"` | Enable OpenShift extensions - one of 'auto', 'true', 'false'. |
| global.exposeServicesMethod | string | `"auto"` | How the Flight Control services should be exposed. Can be either 'auto', 'route', 'gateway' (experimental) or 'none' |
| global.gateway | object | `{"gatewayClassName":"","ports":{"http":80,"tls":443}}` | Configuration for 'gateway' service exposure method |
| global.gateway.gatewayClassName | string | `""` | Gateway API class name for gateway exposure method |
| global.gateway.ports.http | int | `80` | HTTP port for Gateway API configuration |
| global.gateway.ports.tls | int | `443` | TLS port for Gateway API configuration |
| global.generateCertificates | string | `"auto"` | Certificate generation method - one of 'none', 'cert-manager', 'builtin', 'auto'. - none: Do not generate required certificates. The user must provide the Secrets containing the required certificates or the service will fail to start. - cert-manager: Request cert-manager to issue the required certificates. cert-manager must be installed on the cluster. - builtin: Generate required certificates using Helm's builtin library functions. - auto: If cert-manager is installed, use it; otherwise, fall back to builtin. |
| global.imagePullPolicy | string | `"IfNotPresent"` | Image pull policy for all containers |
| global.imagePullSecretName | string | `""` | Name of the secret that holds image pull secret for accessing private container registries |
| global.internalNamespace | string | `""` | A separate Namespace to which non-user-facing components should be deployed for increased security isolation. |
| global.multiclusterEngineNamespace | string | `"multicluster-engine"` | Namespace where MultiCluster Engine is installed. Used for creating discovery ConfigMap and RBAC bindings. |
| global.routeExternalCertificate | string | `"auto"` | Whether to use generated TLS certificates on edge-terminated routes via externalCertificate. - auto: use externalCertificate on fresh install and preserve existing behavior on upgrade. - true: always use externalCertificate. - false: never use externalCertificate (rely on default router cert). |
| global.sshKnownHosts.data | string | `""` | SSH known hosts file content for Git repository host key verification. |
| global.storageClassName | string | `""` | Storage class name for the PVCs. Keep empty to use the default storage class. |
| imageBuilderApi | object | `{"enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-imagebuilder-api-rhel9","pullPolicy":"","tag":""}}` | ImageBuilder API Configuration |
| imageBuilderApi.enabled | bool | `true` | Enable imagebuilder API service |
| imageBuilderApi.image.image | string | `"registry.redhat.io/rhem/flightctl-imagebuilder-api-rhel9"` | ImageBuilder API container image |
| imageBuilderApi.image.pullPolicy | string | `""` | Image pull policy for ImageBuilder API container |
| imageBuilderApi.image.tag | string | `""` | ImageBuilder API image tag |
| imageBuilderWorker | object | `{"defaultTTL":"168h","enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-imagebuilder-worker-rhel9","pullPolicy":"","tag":""},"logLevel":"info","maxConcurrentBuilds":2,"privileged":true,"replicas":1,"resources":{},"rhsmCaSecretName":"","rhsmSecretName":"","sbom":{"enabled":true,"purlTransform":{"enabled":true},"pushToRegistry":true,"uploadToTrustify":true},"serviceImages":{"bootcImageBuilder":{"image":"","skipTlsVerify":false},"podman":{"image":"","skipTlsVerify":false},"syft":{"image":"","skipTlsVerify":false}},"yumReposSecretName":""}` | ImageBuilder Worker Configuration |
| imageBuilderWorker.defaultTTL | string | `"168h"` | Default TTL for image build resources |
| imageBuilderWorker.enabled | bool | `true` | Enable imagebuilder worker service |
| imageBuilderWorker.image.image | string | `"registry.redhat.io/rhem/flightctl-imagebuilder-worker-rhel9"` | ImageBuilder Worker container image |
| imageBuilderWorker.image.pullPolicy | string | `""` | Image pull policy for ImageBuilder Worker container |
| imageBuilderWorker.image.tag | string | `""` | ImageBuilder Worker image tag |
| imageBuilderWorker.logLevel | string | `"info"` | Log level for the imagebuilder worker |
| imageBuilderWorker.maxConcurrentBuilds | int | `2` | Maximum number of concurrent image builds |
| imageBuilderWorker.privileged | bool | `true` | Enable privileged mode for container-in-container builds |
| imageBuilderWorker.replicas | int | `1` | Number of worker replicas |
| imageBuilderWorker.resources | object | `{}` | Resource requests and limits |
| imageBuilderWorker.rhsmCaSecretName | string | `""` | Secret name containing RHSM CA certificates, mounted at /etc/rhsm/ca |
| imageBuilderWorker.rhsmSecretName | string | `""` | Secret name containing RHEL subscription manager configuration, mounted at /etc/rhsm |
| imageBuilderWorker.sbom | object | `{"enabled":true,"purlTransform":{"enabled":true},"pushToRegistry":true,"uploadToTrustify":true}` | SBOM generation after image push (Syft produces CycloneDX JSON; PURL normalization; optional OCI referrer push and Trustify upload when configured). |
| imageBuilderWorker.sbom.enabled | bool | `true` | Run SBOM generation after a successful image push. |
| imageBuilderWorker.sbom.purlTransform.enabled | bool | `true` | Normalize RPM PURLs (namespace/distro/qualifiers) before push/upload for advisory matching. |
| imageBuilderWorker.sbom.pushToRegistry | bool | `true` | Attach the SBOM to the pushed image as an OCI 1.1 referrer artifact on the destination registry. |
| imageBuilderWorker.sbom.uploadToTrustify | bool | `true` | Upload the SBOM to Trustify when vulnerability reporting is enabled and Trustify is configured (same settings as `vulnerabilityReporting` elsewhere in this chart). |
| imageBuilderWorker.serviceImages | object | `{"bootcImageBuilder":{"image":"","skipTlsVerify":false},"podman":{"image":"","skipTlsVerify":false},"syft":{"image":"","skipTlsVerify":false}}` | Builder images (podman, bootc-image-builder, syft) and skip-TLS options |
| imageBuilderWorker.serviceImages.bootcImageBuilder.image | string | `""` | bootc-image-builder image (leave empty to use default). |
| imageBuilderWorker.serviceImages.bootcImageBuilder.skipTlsVerify | bool | `false` | Set to true to skip TLS verification when pulling the bootc-image-builder image. |
| imageBuilderWorker.serviceImages.podman.image | string | `""` | Podman builder image (leave empty to use default). |
| imageBuilderWorker.serviceImages.podman.skipTlsVerify | bool | `false` | Set to true to skip TLS verification when pulling the Podman builder image. |
| imageBuilderWorker.serviceImages.syft.image | string | `""` | Syft image for SBOM generation. If empty, defaults to `docker.io/anchore/syft:v1.44.0`. |
| imageBuilderWorker.serviceImages.syft.skipTlsVerify | bool | `false` | Set to true to skip TLS verification when pulling the Syft image. |
| imageBuilderWorker.yumReposSecretName | string | `""` | Secret name containing yum repository configuration files, mounted at /etc/yum.repos.d |
| kv | object | `{"fsGroup":"","image":{"image":"registry.redhat.io/rhel9/redis-7","pullPolicy":"","tag":"9.7-1766414358"},"loglevel":"warning","maxmemory":"1gb","maxmemoryPolicy":"allkeys-lru","passwordSecretName":""}` | Key-Value Store Configuration |
| kv.fsGroup | string | `""` | File system group ID for Redis pod security context |
| kv.image.image | string | `"registry.redhat.io/rhel9/redis-7"` | Redis container image |
| kv.image.pullPolicy | string | `""` | Image pull policy for Redis container |
| kv.image.tag | string | `"9.7-1766414358"` | Redis image tag |
| kv.loglevel | string | `"warning"` | Redis log level (debug, verbose, notice, warning) |
| kv.maxmemory | string | `"1gb"` | Maximum memory usage for Redis |
| kv.maxmemoryPolicy | string | `"allkeys-lru"` | Redis memory eviction policy |
| kv.passwordSecretName | string | `""` | Secret containing password for Redis password (leave empty for auto-generation) |
| periodic | object | `{"clusterLevelSecretAccess":false,"consumers":5,"image":{"image":"registry.redhat.io/rhem/flightctl-periodic-rhel9","pullPolicy":"","tag":""},"metrics":{"address":":15690","enabled":true}}` | Periodic Configuration |
| periodic.clusterLevelSecretAccess | bool | `false` | Allow flightctl-periodic to list/watch secrets at the cluster level for change detection |
| periodic.consumers | int | `5` | Number of periodic consumers |
| periodic.image.image | string | `"registry.redhat.io/rhem/flightctl-periodic-rhel9"` | Periodic container image |
| periodic.image.pullPolicy | string | `""` | Image pull policy for periodic container |
| periodic.image.tag | string | `""` | Periodic image tag |
| periodic.metrics | object | `{"address":":15690","enabled":true}` | Metrics configuration for flightctl-periodic |
| periodic.metrics.address | string | `":15690"` | Address for the metrics HTTP server |
| periodic.metrics.enabled | bool | `true` | Enable Prometheus metrics endpoint |
| telemetryGateway.additionalRouteLabels | string | `nil` |  |
| telemetryGateway.image.image | string | `"registry.redhat.io/rhem/flightctl-telemetry-gateway-rhel9"` | Telemetry gateway container image |
| telemetryGateway.image.pullPolicy | string | `""` | Image pull policy for Telemetry gateway container |
| telemetryGateway.image.tag | string | `""` | Telemetry gateway image tag |
| ubiMinimal | object | `{"image":"registry.redhat.io/ubi9/ubi-minimal","tag":"9.7-1763362218"}` | UBI Minimal base image used by init containers (cert setup, etc.) Override this when deploying in an air-gapped environment where registry.access.redhat.com is unreachable — set image and tag to the mirrored location produced by the flightctl-mirror-images tool. |
| ubiMinimal.image | string | `"registry.redhat.io/ubi9/ubi-minimal"` | UBI minimal image repository |
| ubiMinimal.tag | string | `"9.7-1763362218"` | UBI minimal image tag (pinned to avoid unexpected updates) |
| ui | object | `{"additionalRouteLabels":null,"auth":{"caCert":"","insecureSkipTlsVerify":false},"enabled":true,"image":{"image":"registry.redhat.io/rhem/flightctl-ui-rhel9","pluginImage":"registry.redhat.io/rhem/flightctl-ui-ocp-rhel9","pullPolicy":"","tag":""},"trustXForwardedHeaders":true,"trustedProxyCidrs":""}` | UI Configuration |
| ui.additionalRouteLabels | string | `nil` | Additional labels for UI routes. |
| ui.auth.caCert | string | `""` | A custom CA cert for Auth TLS. |
| ui.auth.insecureSkipTlsVerify | bool | `false` | Set to true if auth TLS certificate validation should be skipped. |
| ui.enabled | bool | `true` | Enable web UI deployment |
| ui.image.image | string | `"registry.redhat.io/rhem/flightctl-ui-rhel9"` | UI container image |
| ui.image.pluginImage | string | `"registry.redhat.io/rhem/flightctl-ui-ocp-rhel9"` | UI Plugin container image |
| ui.image.pullPolicy | string | `""` | Image pull policy for UI container |
| ui.image.tag | string | `""` | UI container image tag |
| ui.trustXForwardedHeaders | bool | `true` | When true, the UI proxy uses X-Forwarded-Proto and X-Forwarded-Host for OAuth redirect validation (required when TLS terminates at an ingress). Disable if the UI is reached directly without a trusted reverse proxy. Optional trustedProxyCidrs restricts this to listed CIDRs. |
| ui.trustedProxyCidrs | string | `""` | Comma-separated CIDRs for immediate clients that may set forwarded headers (e.g. ingress pod network). Empty means any client when trustXForwardedHeaders is true. |
| upgradeHooks | object | `{"databaseMigrationDryRun":true,"scaleDown":{"condition":"chart","deployments":["flightctl-periodic","flightctl-worker"],"timeoutSeconds":120}}` | Upgrade hooks |
| upgradeHooks.databaseMigrationDryRun | bool | `true` | Enable pre-upgrade DB migration dry-run as a hook |
| upgradeHooks.scaleDown.condition | string | `"chart"` | When to run pre-upgrade scale down job: "always", "never", or "chart" (default). "chart" runs only if helm.sh/chart changed. |
| upgradeHooks.scaleDown.deployments | list | `["flightctl-periodic","flightctl-worker"]` | List of Deployments to scale down in order |
| upgradeHooks.scaleDown.timeoutSeconds | int | `120` | Timeout in seconds to wait for rollout per Deployment |
| vulnerabilityReporting | object | `{"enabled":false,"syncInterval":"15m","trustify":{"auth":{"mode":"none","oidcIssuerUrl":"","secretName":""},"endpoint":""}}` | Vulnerability Integration Configuration |
| vulnerabilityReporting.enabled | bool | `false` | Enable vulnerability integration (sync task + API endpoints). |
| vulnerabilityReporting.syncInterval | string | `"15m"` | Sync interval for periodic Trustify fetch (e.g. "15m", "1h"). |
| vulnerabilityReporting.trustify.auth.mode | string | `"none"` | Authentication mode for Trustify. Allowed values: 'client-credentials', 'none'. |
| vulnerabilityReporting.trustify.auth.oidcIssuerUrl | string | `""` | OIDC issuer URL for client-credentials mode. |
| vulnerabilityReporting.trustify.auth.secretName | string | `""` | Name of the Kubernetes Secret containing 'client_id' and 'client_secret' keys. |
| vulnerabilityReporting.trustify.endpoint | string | `""` | Trustify API base URL (do not include /api/v1 or /api/v2 paths). |
| worker | object | `{"clusterLevelSecretAccess":false,"image":{"image":"registry.redhat.io/rhem/flightctl-worker-rhel9","pullPolicy":"","tag":""}}` | Worker Configuration |
| worker.clusterLevelSecretAccess | bool | `false` | Allow flightctl-worker to access secrets at the cluster level for embedding in device configs |
| worker.image.image | string | `"registry.redhat.io/rhem/flightctl-worker-rhel9"` | Worker container image |
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
