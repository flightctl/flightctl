# flightctl-catalog-collector

Helm chart for the Flight Control catalog collector.

The collector obtains catalog snapshots from configured sources and delivers them through pipelines to destinations. With the `flightctl` destination, it synchronizes Flight Control `Catalog` and `CatalogItem` resources. Other destinations, such as `debug`, can be used to inspect snapshots.

This chart is installed and upgraded independently of the Flight Control service. The collector can run in another namespace or cluster, provided it can reach its configured sources and destinations.

## Install

The chart has no default pipeline. Set exactly one of `config.content` or `config.existingName`; installing without either fails during rendering.

From the repository root, start with the vanilla example:

```shell
helm install catalog-collector deploy/helm/flightctl-catalog-collector \
  --namespace <collector_namespace> \
  --values deploy/helm/flightctl-catalog-collector/examples/values-vanilla.yaml
```

Replace `<collector_namespace>` with an existing namespace.

The `examples/` directory is included in the packaged chart. If you have downloaded the chart archive, extract it:

```shell
tar xf flightctl-catalog-collector-<version>.tgz
```

Then install using the packaged example:

```shell
helm install catalog-collector ./flightctl-catalog-collector \
  --namespace <collector_namespace> \
  --values ./flightctl-catalog-collector/examples/values-vanilla.yaml
```

The vanilla example uses an HTTP snapshot source and a debug destination. It requires no external registry or Flight Control service.

## Configuration

Exactly one of `config.content` and `config.existingName` must be set.

| Value | Meaning |
|---|---|
| `config.content` | Collector configuration supplied as a YAML string. The chart renders it into a ConfigMap and adds a `checksum/config` pod annotation so configuration changes trigger a rollout. The value is processed through Helm's `tpl` function. Empty by default. |
| `config.existingName` | Name of a ConfigMap managed outside the chart, in the collector's namespace. No configuration checksum is added. Restart the Deployment after changing its contents. |
| `config.key` | ConfigMap key containing the configuration. Default: `config.yaml`. |
| `config.mountPath` | Directory where the configuration is mounted. Default: `/etc/flightctl/catalog-collector`. The container starts with `--config <mountPath>/<key>`. |

The chart validates its Helm settings during rendering. The collector validates the collector configuration during startup.

For collector YAML fields, see the [Catalog collector configuration reference](https://github.com/flightctl/flightctl/blob/main/docs/user/references/catalog-collector.md).

### Image

| Value | Meaning |
|---|---|
| `image.image` | Image repository without a tag. The default comes from the chart's build profile. The upstream EL9 profile uses `quay.io/flightctl/flightctl-catalog-collector-el9`. Override it for a mirror or another repository. |
| `image.tag` | Image tag. Empty by default, which uses the chart's `appVersion`. |
| `image.pullPolicy` | Container pull policy. Empty by default, which uses `global.imagePullPolicy`; its default is `IfNotPresent`. |

Set `global.imagePullSecretName` to reference an image pull Secret in the collector's namespace. An empty value attaches no image pull Secret.

`values.yaml` and `Chart.yaml` are generated from the adjacent `.gotmpl` files and the `catalog-collector-<edition>-<os>` profile in [`deploy/helm/helm-chart-opts.yaml`](https://github.com/flightctl/flightctl/blob/main/deploy/helm/helm-chart-opts.yaml).

To change generated defaults or branding, edit those inputs and regenerate from the repository root:

```shell
go generate ./deploy/helm/...
```

Direct edits to the generated files are overwritten during regeneration.

### Replica count

`replicaCount` accepts only the integers `0` and `1`. Other values, including fractional values, fail rendering.

Use `replicaCount: 0` to pause the collector without uninstalling the release.

The Deployment uses the `Recreate` strategy to avoid overlapping old and new pods during an upgrade. With the Flight Control destination, concurrent instances using the same pipeline identifier and organization can update or prune the same resources.

Run only one active collector instance for each pipeline identifier in the target organization. The chart's replica restriction does not coordinate separate Helm releases or collectors deployed elsewhere.

### Service

The chart creates a Service when `service.enabled` is `true` and at least one port is configured through:

* `metrics.enabled`
* `health.enabled`
* `service.extraPorts`

If none of these supplies a port, the Service is omitted.

The default Service type is `ClusterIP`. You can use `kubectl port-forward` against the Deployment when no Service is created.

### Pod labels and annotations

Use `podLabels` and `podAnnotations` to customize the pod template.

The chart protects the following keys:

| Key | Restriction |
|---|---|
| `app.kubernetes.io/name` | A `podLabels` value must match the chart's Deployment selector. Repeating the existing value is accepted. |
| `app.kubernetes.io/instance` | A `podLabels` value must match the release's Deployment selector. Repeating the existing value is accepted. |
| `checksum/config` | `podAnnotations` must not set this key when `config.content` is used. Any override is rejected because the chart owns the configuration checksum. The key can be supplied when using `config.existingName`. |

Other labels and annotations can be supplied through these values.

### Additional ports

Each `service.extraPorts` entry also creates a container port:

```yaml
service:
  extraPorts:
    - name: snapshots
      port: 8080
      targetPort: 8080
      protocol: TCP
```

Each entry must have a non-empty name. Names must be unique across the additional ports and the chart's enabled health and metrics ports.

When health is enabled, its port name is `health`. When metrics are enabled, its port name is `metrics`.

`targetPort`, when supplied, must represent a numeric port because it is also used as the container's `containerPort`. Omit it to use `port`.

The chart does not validate port ranges or every Kubernetes port constraint. Multiple Service ports can target the same container port, and TCP and UDP ports can share a number. Ensure the resulting Service and container ports satisfy Kubernetes validation.

### Health and metrics

Helm values configure ports, probes, and discovery. The collector configuration enables the actual servers. These settings must agree:

| Chart value | Matching collector setting | Chart default |
|---|---|---|
| `health.port` | Port in `extensions.healthcheck.endpoint` | `13133` |
| `health.livePath` | `extensions.healthcheck.livePath` | `/livez` |
| `health.readyPath` | `extensions.healthcheck.readyPath` | `/readyz` |
| `metrics.port` | Port in `service.metrics.endpoint` | `8888` |

Use a bind address reachable through the Pod IP, such as `0.0.0.0` in the shipped examples:

```yaml
service:
  metrics:
    endpoint: 0.0.0.0:8888

extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz
```

The collector's default endpoint addresses use `localhost`. Those addresses are reachable inside the pod network namespace, but not through the Pod IP used by kubelet HTTP probes and ordinary Prometheus scraping.

If the collector configuration omits `healthcheck`, set `health.enabled: false`. Otherwise, enabled probes target an endpoint that is not running.

Similarly, when chart metrics are enabled, configure `service.metrics` with the matching port and a reachable bind address.

Disabling a chart setting removes its chart-managed ports, probes, or discovery configuration. It does not disable an endpoint that remains enabled in the collector YAML.

The metrics path is fixed at `/metrics`. It is not configurable through the chart. A scrape configured for an unsupported path fails, and Prometheus reports the target as unhealthy.

Metrics discovery can use either of these mechanisms:

* `metrics.podAnnotations: true`, the default, adds `prometheus.io/*` pod annotations when metrics are enabled.
* `metrics.serviceMonitor.enabled: true` creates a ServiceMonitor when metrics are enabled. It requires the Prometheus Operator CRDs and `service.enabled: true`.

Set `metrics.serviceMonitor.labels` when your Prometheus instance requires particular labels to select ServiceMonitors.

Collector operational metrics use the `flightctl_catalogcollector_` prefix. Exporter metadata metrics can use other names. Scrape the endpoint to inspect the series exposed by your collector version.

Neither the health endpoint nor the metrics endpoint authenticates callers. A ClusterIP Service does not authenticate clients either. Restrict access as appropriate for your environment, and use an authenticating proxy with TLS if exposing these endpoints beyond trusted networks.

### Security context

The default security settings run the collector as non-root with:

* A read-only root filesystem.
* Privilege escalation disabled.
* All Linux capabilities dropped.
* The `RuntimeDefault` seccomp profile.

These defaults are compatible with OpenShift's `restricted-v2` SCC. The shipped collector components require no writable scratch volume.

### Credentials and CA bundles

Mount credential files and CA bundles using `extraVolumes` and `extraVolumeMounts`. Reference their paths inside the container through fields such as:

* `tokenFile`
* `clientIdFile`
* `clientSecretFile`
* `certificateAuthority`

For example:

```yaml
extraVolumes:
  - name: oauth
    secret:
      secretName: flightctl-catalog-collector-oauth
      defaultMode: 0440

extraVolumeMounts:
  - name: oauth
    mountPath: /etc/flightctl/catalog-collector/oauth
    readOnly: true
```

The Secret must exist in the collector's namespace.

Values can also be supplied through `env` or `envFrom` and referenced as `${env:NAME}` in supported collector configuration fields. Environment references are resolved during startup; changing them requires restarting the collector.

#### File permissions

The official container images specify UID `1001` and GID `0`. OpenShift can assign a UID from the namespace's permitted range.

Use group-readable credential files, such as `defaultMode: 0440` in the Secret volume examples. This avoids depending on a fixed container UID.

File access also depends on the pod's effective groups, admission policy, and volume ownership handling. An `fsGroup` can affect mounted ownership and permissions, so do not assume that every Secret volume always remains `root:root` or that `0400` behaves identically in every deployment.

The chart does not require an explicit `fsGroup` for its documented image and mounts. If you customize group settings, ensure they satisfy the cluster's admission policy and allow access to the mounted files. Do not assume that `fsGroup: 0` is permitted on OpenShift.

For standalone bind-mount paths and host file permissions, see the [standalone README](https://github.com/flightctl/flightctl/blob/main/deploy/podman/flightctl-catalog-collector/README.md).

### Authenticating to Flight Control

The Flight Control destination can use an authentication extension implementing `extensionauth.HTTPClient`.

For OAuth 2.0 client credentials:

```yaml
extensions:
  oauth2client/flightctl:
    clientIdFile: /etc/flightctl/catalog-collector/oauth/client-id
    clientSecretFile: /etc/flightctl/catalog-collector/oauth/client-secret
    tokenUrl: https://sso.example.com/realms/flightctl/protocol/openid-connect/token
    scopes:
      - openid
    certificateAuthority: /etc/flightctl/catalog-collector/certs/oidc-ca.crt
    expiryBuffer: 30s

destinations:
  flightctl/service:
    server: https://flightctl.example.com
    auth:
      authenticator: oauth2client/flightctl
```

The token URL above is a Keycloak example. Use your authorization server's actual token endpoint, usually published as `token_endpoint` in its OpenID Connect discovery document. Configure scopes according to that provider.

The extension obtains and caches an access token using the client-credentials grant. `expiryBuffer` causes replacement before the token's expiry, reducing the chance of expiry during a request. It does not guarantee that requests cannot receive `401 Unauthorized`.

Authorize the client to read/list, create, update, and delete both Catalog and CatalogItem resources in the target organization. Delete permission is required for pruning.

Prefer `clientIdFile` and `clientSecretFile` to inline credentials. Literal inline values are stored in the rendered ConfigMap and Helm release values.

Credential files are read when a new token is acquired. Rotating those files takes effect when the cached token is next replaced.

## Authorizing against Red Hat OpenShift AI

For a Model Registry protected by a Kubernetes RBAC proxy, the collector needs a token accepted by the registry and a RoleBinding granting that identity access.

The following setup uses the collector pod's ServiceAccount and assumes the collector runs in the same cluster as the registry. For another cluster, see [Collecting from another cluster](#collecting-from-another-cluster).

### 1. Create the ServiceAccount

Configure a named ServiceAccount and enable token automount:

```yaml
serviceAccount:
  create: true
  name: flightctl-catalog-collector
  automountServiceAccountToken: true
```

The kubelet mounts the pod-bound ServiceAccount token at:

```text
/var/run/secrets/kubernetes.io/serviceaccount/token
```

The kubelet renews this token before it expires.

The chart defaults to `automountServiceAccountToken: false`. Enable it when the collector needs its local ServiceAccount identity.

### 2. Grant access to the model registry

The Model Registry operator provides a `registry-user-<model_registry_name>` Role granting access to the registry's Service.

Bind the collector's ServiceAccount to that Role:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: flightctl-catalog-collector-registry-user
  namespace: <model_registry_namespace>
subjects:
  - kind: ServiceAccount
    name: flightctl-catalog-collector
    namespace: <collector_namespace>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: registry-user-<model_registry_name>
```

The RoleBinding belongs in the registry namespace. Its subject identifies the collector's ServiceAccount and namespace.

Check access to the specific Service named in the Role's rules:

```shell
kubectl auth can-i get services/<model_registry_service_name> \
  --as=system:serviceaccount:<collector_namespace>:flightctl-catalog-collector \
  --namespace <model_registry_namespace>
```

Replace `<model_registry_service_name>` with the actual Service name. Checking unnamed `services` can return `no` when the Role permits access only to a named Service.

The collector performs read requests against the registry. The Role grants access through the registry's authorization mechanism; it does not by itself describe permissions for individual Model Registry API operations.

### 3. Configure bearer authentication

Point the bearer token extension at the automounted token:

```yaml
extensions:
  bearertokenauth/model-registry:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token

sources:
  kubeflowmodelregistry/rhoai:
    endpoint: https://model-registry.example.com
    catalog: ai-models
    auth:
      authenticator: bearertokenauth/model-registry
```

Configure a CA bundle when the registry certificate is not trusted by the system trust store.

The bearer token extension reads the file on every request, so kubelet token rotation takes effect without restarting the collector.

The token audience must match what the registry's authentication proxy accepts. The automounted token uses the API server's default audience. Check the deployed proxy's requirements before choosing a custom audience.

#### Projecting the token explicitly

Use an explicit projection when you need to choose the mounted path or request a token lifetime:

```yaml
extraVolumes:
  - name: registry-token
    projected:
      defaultMode: 0440
      sources:
        - serviceAccountToken:
            path: model-registry-token
            expirationSeconds: 3600

extraVolumeMounts:
  - name: registry-token
    mountPath: /var/run/secrets/model-registry
    readOnly: true
```

Reference the projected file:

```yaml
extensions:
  bearertokenauth/model-registry:
    tokenFile: /var/run/secrets/model-registry/model-registry-token
```

Omitting `audience` requests the API server's default audience. Set it explicitly only when required by the registry's authentication configuration.

The kubelet rotates projected tokens. This volume is independent of `automountServiceAccountToken`, so automount can remain disabled when using an explicit projection.

#### Collecting from another cluster

Supply a credential issued by, and accepted by, the registry cluster. Do not assume that the collector cluster's local ServiceAccount token is accepted there.

In the registry cluster, create a ServiceAccount and bind it to the registry Role. For example, create it in the registry namespace:

```shell
kubectl --context <registry_cluster> \
  --namespace <model_registry_namespace> \
  create serviceaccount flightctl-catalog-collector
```

Apply the RoleBinding from the previous section in that cluster, setting its subject namespace to `<model_registry_namespace>` for this ServiceAccount.

Request a token. Create the file with restrictive permissions first, so the token is never readable by other users on the host:

```shell
install -m 0600 /dev/null model-registry-token
kubectl --context <registry_cluster> \
  --namespace <model_registry_namespace> \
  create token flightctl-catalog-collector --duration=24h \
  > model-registry-token
```

The requested duration is not a guarantee: the API server can issue a token with a different lifetime. Arrange rotation based on the issued token's actual expiry.

Create a Secret in the collector cluster and namespace:

```shell
kubectl --context <collector_cluster> \
  --namespace <collector_namespace> \
  create secret generic flightctl-catalog-collector-tokens \
  --from-file=model-registry-token
```

Remove the local token file once the Secret exists:

```shell
shred -u model-registry-token
```

Mount it:

```yaml
extraVolumes:
  - name: tokens
    secret:
      secretName: flightctl-catalog-collector-tokens
      defaultMode: 0440

extraVolumeMounts:
  - name: tokens
    mountPath: /etc/flightctl/catalog-collector/tokens
    readOnly: true
```

Configure the extension:

```yaml
extensions:
  bearertokenauth/model-registry:
    tokenFile: /etc/flightctl/catalog-collector/tokens/model-registry-token
```

Keep `automountServiceAccountToken: false` unless another component requires the collector cluster's local token.

A manually issued token is not renewed by the kubelet. Refresh the Secret before expiry, allowing time for the mounted contents to update. Secret updates propagate eventually; the delay depends on kubelet synchronization and change detection.

The bearer token extension uses the refreshed file on subsequent requests without a restart. Do not use a `subPath` mount for a rotating token file because such mounts do not receive automatic Secret updates.

### 4. Verify

The Kubeflow Model Registry source performs two startup queries, checking access to the registered-model and model-version endpoints with their configured filters.

When using the `flightctl` destination, its preflight runs first and checks read-only list access to Catalogs and CatalogItems. It does not verify create, update, or delete permissions.

A preflight failure stops startup before sources run or readiness is reported. Inspect the Deployment and logs:

```shell
kubectl --namespace <collector_namespace> rollout status \
  deployment/catalog-collector-flightctl-catalog-collector
```

```shell
kubectl --namespace <collector_namespace> logs \
  deployment/catalog-collector-flightctl-catalog-collector
```

These commands assume the release name `catalog-collector` and the upstream chart's default naming. Adjust the Deployment name if either differs.

Readiness confirms that startup checks passed; it does not confirm a successful import. The first collection starts immediately after startup. Later successful cycles are separated by `pollInterval`; failures use retry backoff.

To inspect metrics, run port-forwarding in one terminal and leave it running:

```shell
kubectl --namespace <collector_namespace> port-forward \
  deployment/catalog-collector-flightctl-catalog-collector 8888:8888
```

In another terminal:

```shell
curl -fsS http://127.0.0.1:8888/metrics \
  | grep '^flightctl_catalogcollector_'
```

Check that collection and pipeline synchronization success counters advance for the expected source and pipeline. A successful collection alone does not establish that synchronization succeeded.

For metric descriptions, see [Metrics](https://github.com/flightctl/flightctl/blob/main/docs/user/references/catalog-collector.md#metrics). For checking imported resources, see [Verifying the import](https://github.com/flightctl/flightctl/blob/main/docs/user/using/catalog-collector/kubeflow-model-registry.md#verifying-the-import).

## Examples

| File | Pipeline |
|---|---|
| [`examples/values-vanilla.yaml`](examples/values-vanilla.yaml) | HTTP snapshot source to debug destination. No external dependencies. |
| [`examples/values-rhoai-to-flightctl.yaml`](examples/values-rhoai-to-flightctl.yaml) | Kubeflow Model Registry to Flight Control. Uses the pod's ServiceAccount token for a registry in the same cluster, OAuth 2.0 client credentials for Flight Control, CA bundles, and selection filters. |

Equivalent standalone configurations are available in [`deploy/podman/flightctl-catalog-collector/examples`](https://github.com/flightctl/flightctl/tree/main/deploy/podman/flightctl-catalog-collector/examples).

## Resource requests

The chart defaults are:

| Resource | Request | Limit |
|---|---|---|
| CPU | `50m` | `500m` |
| Memory | `64Mi` | `512Mi` |

Adjust these values for your workload. The collector holds complete snapshots in memory, and processors and reconciliation can require additional copies or resource lists. Memory requirements depend on snapshot size and pipeline configuration.

## Validation

Run chart linting from the repository root:

```shell
make lint-helm
```

Run the collector chart's render tests:

```shell
make test-helm-catalog-collector
```

Inspect the rendered vanilla Deployment:

```shell
helm template catalog-collector deploy/helm/flightctl-catalog-collector \
  --namespace <collector_namespace> \
  --kube-version 1.32.0 \
  --values deploy/helm/flightctl-catalog-collector/examples/values-vanilla.yaml
```

The render tests cover invalid value combinations and expected manifests, including:

* Missing or conflicting configuration sources.
* Invalid replica counts.
* Service omission when there are no ports.
* Conflicting Deployment selector labels.
* Reserved configuration checksum annotations.
* Duplicate port names.
* Missing extra-port names and unsupported named target ports.
* Rendering the shipped example values.

The `Helm Chart Quality` workflow runs linting and render tests when its change detection matches `deploy/helm/**`, `Makefile`, or `.github/workflows/lint-helm.yaml`.

Rendering checks do not establish connectivity, credential validity, or successful synchronization. Verify those against the deployed collector.
