# Catalog collector configuration reference

This reference describes the configuration fields and built-in components of the Flight Control catalog collector. For concepts and workflows, see [Catalog collector overview](../using/catalog-collector/overview.md).

Supply the YAML configuration file with the required `--config` option:

```console
flightctl-catalog-collector --config <path>
```

The collector rejects unknown configuration fields. Fields are checked as follows:

* Top-level keys and fields under `service` and `pipelines` are checked when the configuration file is loaded.
* Fields in source, processor, and destination configurations are checked only when a pipeline references those components.
* Fields in every configured extension are checked, whether or not another component references it.

Unused sources, processors, and destinations remain inactive, and their component-specific fields are not validated. Component identifiers and pipeline references are still checked when the file is loaded.

A configuration error found during these checks stops startup. For the available sections and component activation rules, see [Top-level sections](#top-level-sections).

## Top-level sections

| Section | Required | Description |
|---|---|---|
| `service` | No | Process-wide settings, including the log level and metrics endpoint. |
| `extensions` | No | Shared capabilities, such as health checks and authentication. |
| `sources` | Yes | Named source configurations. Every pipeline references one source. |
| `processors` | No | Named processor configurations for optional transformations. |
| `destinations` | Yes | Named destination configurations. Every pipeline references one destination. |
| `pipelines` | Yes | Named pipelines. At least one pipeline must be defined. |

Sources, processors, and destinations are activated only when referenced by a pipeline. Every configured extension is activated, whether or not another component references it.

## Component identifiers

Each key under `sources`, `processors`, `destinations`, and `extensions` is a component identifier in the form `type[/name]`.

The type selects the implementation. The optional name distinguishes multiple configurations of the same component type.

The following examples use the built-in HTTP source type:

| Rule | Example |
|---|---|
| Type only | `http` |
| Type with an instance name | `http/snapshots` |
| An empty identifier is rejected | `""` |
| An empty type is rejected | `/snapshots` |
| An empty name after the separator is rejected | `http/` |
| More than one separator is rejected | `http/a/b` |

Identifiers must be unique within each configuration section. The same identifier can be reused in different sections.

## Environment variable references

You can use environment variables for string values inside configurations under `sources`, `processors`, `destinations`, and `extensions`.

For example:

```yaml
destinations:
  flightctl/service:
    server: ${env:FLIGHTCTL_SERVER}
```

Set `FLIGHTCTL_SERVER` to the complete API URL, such as `https://flightctl.example.com`.

The following rules apply:

| Rule | Behavior |
|---|---|
| Accepted forms | `${env:NAME}` and `${NAME}`. |
| Variable names | Must start with a letter or underscore and contain only letters, digits, and underscores. |
| Placement | The reference must replace the entire string value. Expressions such as `https://${HOST}/api` remain literal text. |
| Configuration scope | References are expanded only inside component configurations. They are not expanded under `service` or `pipelines`. |
| Mapping keys | Keys are never expanded. |
| Malformed references | Whole-value expressions such as `${env:}` or `${NAME:-default}` cause a startup error when resolved. |
| Unset variables | Startup fails with an error naming the variable and its configuration field. |

References are resolved during startup when components are initialized. References in unused sources, processors, and destinations are not resolved. References in every configured extension are resolved.

Reference-resolution errors do not include resolved environment-variable values.

## service

```yaml
service:
  logLevel: info
  metrics:
    endpoint: 127.0.0.1:8888
```

| Field | Type | Default | Description |
|---|---|---|---|
| `logLevel` | string | Initial logger level (`info` in the official collector) | Accepts `debug`, `info`, `warn`, or `error`. Omit the field to preserve the initial logger level. An explicit empty string is rejected. |
| `metrics` | object | Absent | Enables the Prometheus metrics endpoint. Omit the block to disable metrics. An empty block enables metrics with the default endpoint. |
| `metrics.endpoint` | string | `localhost:8888` | Listener address in `host:port` format. Both host and port are required when an address is supplied. An omitted or empty value uses the default. |

The metrics path is fixed at `/metrics` and is not configurable.

To expose metrics through a published container port or a Kubernetes Service, set `metrics.endpoint` to `0.0.0.0:8888`. The default listens only on loopback.

## pipelines

Each key under `pipelines` is a pipeline ID, such as `external-catalog`:

```yaml
pipelines:
  external-catalog:
    source: http/snapshots
    processors:
      - catalogname/rename
    destination: flightctl/service
```

| Field | Type | Required | Description |
|---|---|---|---|
| `source` | string | Yes | Identifier of a source defined under `sources`. |
| `processors` | list of strings | No | Identifiers of processors defined under `processors`, applied in the listed order. |
| `destination` | string | Yes | Identifier of a destination defined under `destinations`. |

The configuration must define at least one pipeline. Each pipeline must specify a source and a destination, and every referenced component must be defined.

Several pipelines can share a source. The collector delivers each snapshot to those pipelines sequentially. Each pipeline applies its own processors and validates the resulting snapshot before passing it to its destination.

Several pipelines can also share a destination instance. Resource management and concurrency behavior depend on the destination implementation.

For the `flightctl` destination:

* Collector labels identify the resources managed by each pipeline.
* Reconciliation for the same pipeline ID is serialized within that destination instance.
* The pipeline ID must be non-empty and valid as a Kubernetes label value, because it is written into a management label. This requirement is checked when reconciliation begins.

> [!IMPORTANT]
> Pipelines targeting the same Flight Control organization must use distinct pipeline IDs and manage separate catalogs.
>
> Run only one active collector instance for each pipeline ID in that organization. Reconciliation locks do not coordinate separate collector instances.

For resource management rules, see [Flight Control destination](#flightctl).

## Sources

### http

Receives complete catalog snapshots posted to an HTTP endpoint. Use it with an external adapter that converts platform content into Catalog and CatalogItem resources, or for manual pipeline testing.

```yaml
sources:
  http/snapshots:
    listenAddress: 127.0.0.1:8080
    path: /v1/snapshots
```

| Field | Type | Default | Description |
|---|---|---|---|
| `listenAddress` | string | `127.0.0.1:8080` | HTTP listener address. An explicit empty string is rejected. |
| `path` | string | `/v1/snapshots` | Endpoint path. Must be non-empty, start with `/`, and contain neither `{` nor `}`. |

To access the source through a published container port or a Kubernetes Service, configure `listenAddress` to listen on `0.0.0.0`, for example `0.0.0.0:8080`.

#### Request requirements

Send a `POST` request to the configured path with a JSON snapshot as its body.

The `Content-Type` media type must be `application/json`. Parameters are accepted, for example `application/json; charset=utf-8`.

The snapshot must contain all three fields:

| Field | Type | Requirement |
|---|---|---|
| `revision` | string | Must be non-empty. |
| `catalogs` | array | Must be present and must not be `null`. Use `[]` for an empty collection. |
| `catalogItems` | array | Must be present and must not be `null`. Use `[]` for an empty collection. |

The maximum request body size is 16 MiB. Unknown fields in the snapshot or resource objects are rejected.

Setting both arrays to `[]` represents an empty desired state. For a pipeline using the Flight Control destination, this requests deletion of all resources managed by that pipeline.

For the shared snapshot structure and an example, see [Snapshot format](../using/catalog-collector/overview.md#snapshot-format).

#### Responses

| Response | Meaning |
|---|---|
| `405 Method Not Allowed` | The request method is not `POST`. |
| `415 Unsupported Media Type` | The `Content-Type` header is missing, malformed, or specifies a media type other than `application/json`. |
| `400 Bad Request` | The body exceeds the size limit, cannot be read or decoded, contains unknown fields, or fails the required-field checks above. |
| `502 Bad Gateway` | Snapshot validation, a processor, or a destination returned an error. |
| `204 No Content` | Every pipeline using this source reported success. |

The HTTP handler checks JSON decoding and the required snapshot fields. Resource validation runs inside the pipeline, so a snapshot that decodes successfully but fails resource validation returns `502 Bad Gateway`.

A `204 No Content` response confirms pipeline success. The result depends on the destination: `flightctl` reconciles resources in Flight Control, while `debug` logs the snapshot. For Flight Control pipelines, verify the resulting resources with the Flight Control CLI.

> [!WARNING]
> The `http` source does not authenticate incoming requests. Clients that can reach it can submit snapshots that change or delete resources managed by pipelines using the Flight Control destination.
>
> Bind the endpoint to loopback for local use. For remote access, restrict network access and use an authenticating proxy with TLS.

### kubeflowmodelregistry

Polls a Kubeflow Model Registry and converts registered model metadata into complete snapshots containing Catalog and CatalogItem resources. Supported registries include Model Registry instances running in Red Hat OpenShift AI.

For the workflow and model-mapping rules, see [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md).

```yaml
sources:
  kubeflowmodelregistry/rhoai:
    endpoint: https://model-registry.example.com
    catalog: rhoai-model-registry
    pollInterval: 5m
    auth:
      authenticator: bearertokenauth/model-registry
```

The example references an authenticator that must be defined under `extensions`.

| Field | Type | Default | Description |
|---|---|---|---|
| `endpoint` | string | None | Required. Absolute `http` or `https` base URL of the Model Registry REST API. Must not contain user information, query parameters, a literal `#`, or leading or trailing whitespace. |
| `catalog` | string | None | Required. Name of the Catalog resource produced by this source. Must be a valid Flight Control resource name. |
| `pollInterval` | duration | `5m` | Delay after a cycle in which collection and downstream pipeline processing both succeed. Must be positive. |
| `pageSize` | integer | `100` | Number of items requested per paginated API call. Must be between 0 and 2147483647. A value of `0` uses the default. |
| `requestTimeout` | duration | `30s` | Timeout for each Model Registry HTTP request. Must be positive. |
| `collectionTimeout` | duration | `5m` | Timeout for collecting a complete snapshot, including pagination and normalization. Excludes downstream pipeline processing. Must be positive. |
| `selection.modelFilter` | string | `state='LIVE'` | Filter sent as `filterQuery` when listing registered models. Set to `""` to omit this filter. Version filtering and model-mapping rules still apply. |
| `selection.versionFilter` | string | `state='LIVE'` | Filter sent as `filterQuery` when listing model versions. Set to `""` to omit this filter. |
| `auth.authenticator` | string | None | Identifier of an authentication extension, such as `bearertokenauth/model-registry`. Required when `auth` is configured. Requires an `https` endpoint. When `auth` is omitted, requests are unauthenticated. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle used to verify the registry certificate. Certificates from the bundle are added to the system trust roots. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS certificate verification. Intended for development only. Enabling it produces a startup warning. |
| `backoff.initialInterval` | duration | `1s` | Base retry delay after the first failed cycle. Must be positive. |
| `backoff.maxInterval` | duration | `5m` | Maximum retry delay, including jitter. Must be positive and at least `backoff.initialInterval`. |
| `backoff.multiplier` | number | `2` | Multiplier applied to the base retry delay after each consecutive failure. Must be finite and at least `1.0`. |
| `backoff.randomizationFactor` | number | `0.5` | Factor controlling symmetric jitter around the base retry delay. Must be finite and between 0 and 1 inclusive. |

Configure `endpoint` with the registry base URL and any reverse-proxy prefix. The collector appends the Model Registry API path.

#### Polling and retries

The first collection cycle starts immediately when the source starts. Collection cycles do not overlap.

After collection and downstream pipeline processing succeed, the source resets the retry backoff and waits for `pollInterval`. A collection or downstream pipeline failure triggers exponential retry backoff with jitter.

If collection fails, the source reports the error without emitting a partial snapshot.

#### Startup preflight

This source implements startup preflight. It sends read-only list requests to the registered-models and model-versions endpoints, requesting one result from each. Each request includes its configured filter when that filter is non-empty.

These checks verify registry access and acceptance of the configured filters. Empty result lists are accepted. A request failure or a response missing the required `items` field stops startup before the collector reports ready.

Destination checks are performed separately by destinations that implement preflight. Retry backoff applies to runtime polling cycles; startup preflight failures stop startup.

## Processors

### catalogname

Renames Catalog resources within a snapshot and updates the `metadata.catalog` field of matching CatalogItem resources.

```yaml
processors:
  catalogname/rename:
    mappings:
      rhoai-model-registry: ai-models
```

| Field | Type | Default | Description |
|---|---|---|---|
| `mappings` | map of string to string | None | Required and non-empty. Each key is an input catalog name and each value is its replacement. Keys and values must be non-empty. Replacement values must be unique. |

Catalog names without a mapping pass through unchanged.

Mappings are applied once to each input name. For example, with mappings `a: b` and `b: c`, an input catalog named `a` becomes `b`.

If the resulting snapshot contains duplicate Catalog names or duplicate CatalogItem identities—the same catalog and item name—the processor returns an error. The snapshot is not forwarded to subsequent stages of that pipeline.

## Destinations

### flightctl

Synchronizes Catalog and CatalogItem resources in Flight Control with the complete desired content supplied by each pipeline.

```yaml
destinations:
  flightctl/service:
    server: https://flightctl.example.com
    certificateAuthority: /etc/flightctl/catalog-collector/certs/flightctl-ca.crt
    timeout: 30s
    auth:
      authenticator: oauth2client/flightctl
```

The example references an authenticator that must be defined under `extensions`.

| Field | Type | Default | Description |
|---|---|---|---|
| `server` | string | None | Required. Base URL of the Flight Control service. The destination removes a trailing slash and appends `/api/v1`. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle. Its certificates are added to the system trust roots. The file must contain at least one valid PEM certificate. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS certificate verification. Intended for development only. |
| `orgId` | string | Organization selected by the service | Target organization. A non-empty value must be a valid UUID. |
| `auth.authenticator` | string | None | Identifier of a configured HTTP authentication extension. Must be non-empty when `auth` is configured. When `auth` is omitted, requests are unauthenticated. |
| `timeout` | duration | `30s` | Per-request timeout for Flight Control API calls. Must be positive when configured. |

Configure `server` without the `/api/v1` suffix.

HTTPS connections require TLS 1.3 or later. Proxy settings are taken from the standard proxy environment variables.

#### Startup preflight

This destination implements startup preflight. It sends one read-only list request for Catalogs and one for CatalogItems, requesting one result from each collection.

Preflight uses the same client as synchronization, including its authentication, TLS settings, target organization, timeout, and API version.

Empty collections are accepted. A request failure or a response missing the required `items` field stops startup before sources run and before the collector reports ready.

The checks verify connectivity, authentication, and list permissions. They do not verify create, update, or delete permissions.

#### Required permissions

For normal synchronization, the configured identity must be allowed to read and list, create and update, and delete Catalog and CatalogItem resources in the target organization.

Delete permission is required whenever synchronization needs to remove resources omitted from a snapshot.

#### Resource management

The destination adds the following labels to resources it manages:

| Label | Value |
|---|---|
| `flightctl.io/managed-by` | `flightctl-catalog-collector` |
| `flightctl.io/catalog-collector-pipeline` | The pipeline ID from the configuration |

These labels identify each pipeline's management scope within the target organization.
Keep pipeline IDs stable. Renaming a pipeline leaves its existing resources labeled with the old ID. Those resources are outside the renamed pipeline’s management scope, so attempting to synchronize the same resource names causes a name collision.

The destination applies the following rules:

* Missing resources are created. Catalogs are created before their CatalogItems.
* Existing resources are updated when their specifications or labels differ from the desired content.
* Resources whose specifications and labels already match are left unchanged.
* An existing resource must carry both management labels with the expected values for that pipeline. A desired name that collides with a resource outside this scope causes synchronization to fail.
* Existing resources with a non-empty `metadata.owner`, such as resources owned by ResourceSync, are not managed.
* Snapshots must not set either reserved management label. The destination adds them automatically and rejects snapshots that supply them.

The reserved-label check is specific to this destination.

#### Pruning and synchronization failures

The destination attempts to delete pipeline-managed resources omitted from the desired snapshot. A snapshot with no Catalogs and no CatalogItems requests deletion of all resources managed by that pipeline.

Pruning starts only after:

* All desired resource writes succeed.
* The complete lists of pipeline-managed Catalogs and CatalogItems are retrieved.
* The listed resources pass the destination's management-scope checks.

CatalogItems are deleted before Catalogs.

Synchronization is not transactional. If a write fails, earlier writes remain applied. If a deletion fails, earlier deletions remain applied and subsequent deletions are not attempted.

A later successful synchronization can complete the remaining changes. Replaying a snapshot with the same revision can also repair differences between the desired and actual resources.

#### API protections

The Flight Control API enforces additional restrictions:

* A CatalogItem cannot be deleted while a device or fleet references one of its versions. The API returns a conflict identifying the referenced versions.
* An update cannot remove or modify a CatalogItem version that is referenced by a device or fleet.
* A Catalog cannot be deleted while it contains any CatalogItems, including items outside the pipeline's management scope.
* Direct specification changes and deletion are rejected for resources with an owner. Label-only updates are permitted.

These restrictions can prevent synchronization or pruning from completing. Resolve the reported conflict before attempting synchronization again.

#### Manual changes

Collector labels identify what the destination manages. Authorized API clients can still edit or delete labeled resources, subject to the API protections above.

While the management labels remain intact, the next successful synchronization restores the pipeline's desired specifications and labels. Make changes to imported specifications in the source system.

Removing or changing the management labels takes the resource outside the pipeline's management scope and can cause a name collision during subsequent synchronization.

ResourceSync uses `metadata.owner` for API-enforced ownership. For a comparison, see [Importing catalogs from external sources](../using/managing-catalogs.md#importing-catalogs-from-external-sources).

### debug

Logs snapshots through the collector’s logger. Use it to inspect snapshot content after any configured processors have run.

```yaml
destinations:
  debug/inspect:
    verbosity: normal
```

| Field | Type | Default | Description |
|---|---|---|---|
| `verbosity` | string | `basic` | One of `basic`, `normal`, or `detailed`. An explicit empty string is rejected. |

| Value | Logged content |
|---|---|
| `basic` | Pipeline identifier, revision, and resource counts. |
| `normal` | The basic summary plus sorted catalog names and catalog item identities in `catalog/name` format. |
| `detailed` | The basic summary followed by one entry per resource, including its full JSON representation. Catalogs are sorted by name, and catalog items by `catalog/name`. |

Snapshot log entries are emitted at `info` level. Set `service.logLevel` to `info` or `debug` to see them. Setting it to `warn` or `error` suppresses these entries, regardless of `verbosity`.

Enable `detailed` deliberately: the full resource content may contain sensitive operational information.

## Extensions

### healthcheck

Serves HTTP endpoints for liveness and readiness probes.

```yaml
extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz
```

| Field | Type | Default | Description |
|---|---|---|---|
| `endpoint` | string | `localhost:13133` | Address the health server binds. Must be a non-empty `host:port` pair with both parts specified. |
| `livePath` | string | `/livez` | Path for liveness probes. |
| `readyPath` | string | `/readyz` | Path for readiness probes. |

Both paths must be non-empty, start with `/`, and be canonical: no repeated slashes, `.` or `..` segments, or trailing slash except for `/` itself. They must not contain `?`, `#`, `%`, `{`, or `}`. The two paths must differ.

Both endpoints accept `GET` and `HEAD`. Other methods return `405 Method Not Allowed`.

| Endpoint | Response |
|---|---|
| Liveness | `200 OK` while the health server is serving requests. |
| Readiness | `200 OK` when the collector is marked ready; otherwise, `503 Service Unavailable`. |

The collector is marked ready after extensions have started, all applicable source and destination preflight checks have succeeded, and every source goroutine has been launched. Readiness does not wait for the first successful snapshot.

Readiness reflects the collector’s startup lifecycle. It does not continuously check external dependencies or track collection and reconciliation results. A polling source can continue retrying failed synchronization cycles while readiness remains `200 OK`.

During shutdown, the collector is marked not ready after sources stop and before extensions shut down.

For Kubernetes probes or access through a published container port, set `endpoint` to `0.0.0.0:13133`. The default binds loopback within the container’s network namespace.

### bearertokenauth

Adds an `Authorization: Bearer <token>` header to outgoing HTTP requests from components that reference it through `auth.authenticator`.

```yaml
extensions:
  bearertokenauth/model-registry:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
```

| Field | Type | Default | Description |
|---|---|---|---|
| `token` | string | None | Inline token value. Mutually exclusive with `tokenFile`. Surrounding whitespace is trimmed before use. |
| `tokenFile` | string | None | Path to a file containing the token. Mutually exclusive with `token`. |

Exactly one non-empty value must be supplied. Empty or whitespace-only values are treated as unset during configuration validation. The extension does not log token values.

When `tokenFile` is used, the file is read on every request and surrounding whitespace is trimmed from its contents. Token rotation at the configured path takes effect without restarting the collector.

A missing, unreadable, or empty token file causes the request to fail before it is sent. The extension does not read the file during its own startup, but a source or destination preflight request can detect the problem and stop collector startup.

### oauth2client

Obtains access tokens using the OAuth 2.0 client-credentials grant and attaches them to outgoing HTTP requests from components that reference it through `auth.authenticator`.

```yaml
extensions:
  oauth2client/flightctl:
    clientIdFile: /etc/flightctl/catalog-collector/oauth/client-id
    clientSecretFile: /etc/flightctl/catalog-collector/oauth/client-secret
    tokenUrl: https://sso.example.com/realms/flightctl/protocol/openid-connect/token
    scopes:
      - openid
    certificateAuthority: /etc/flightctl/catalog-collector/certs/oidc-ca.crt
    timeout: 30s
    expiryBuffer: 30s
```

| Field | Type | Default | Description |
|---|---|---|---|
| `clientId` | string | None | Client identifier. Mutually exclusive with `clientIdFile`; exactly one is required. |
| `clientIdFile` | string | None | Path to a file containing the client identifier. |
| `clientSecret` | string | None | Client secret. Mutually exclusive with `clientSecretFile`; exactly one is required. |
| `clientSecretFile` | string | None | Path to a file containing the client secret. |
| `tokenUrl` | string | None | Required. Absolute `http` or `https` token endpoint with a host. Must not contain user information or a fragment. |
| `scopes` | list of strings | None | Scopes requested from the authorization server. Entries must not be empty or whitespace-only. |
| `endpointParams` | map of string to string | None | Additional token request parameters. Names must not be empty or whitespace-only. `client_id`, `client_secret`, `grant_type`, and `scope` are reserved; validation ignores case and surrounding whitespace when checking these names. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle for the token endpoint. Certificates are added to the system roots. The file must contain at least one valid PEM certificate. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS verification for token requests. Development only. |
| `timeout` | duration | `30s` | Per-request timeout for token endpoint requests. Must be greater than zero. |
| `expiryBuffer` | duration | `10s` | Treats a cached token as expired this long before its actual expiry. Must not be negative. In the current implementation, `0s` also uses a 10-second buffer. |

The example uses a Keycloak realm token endpoint. Configure `tokenUrl` and `scopes` for your authorization server. For an OpenID Connect provider, obtain the endpoint from the discovery document’s `token_endpoint` field.

The TLS and timeout settings in this extension apply to the token endpoint. Configure the source or destination’s connection settings separately. HTTPS token requests require TLS 1.2 or later.

Tokens are acquired lazily and cached. Before sending an authenticated request, the extension checks the cached token and obtains a replacement if it is missing or considered expired. Token acquisition can therefore occur during a source or destination startup preflight.

The expiry buffer reduces the chance that a token expires while a request is in flight. A `401 Unauthorized` response does not automatically trigger token renewal or replay of the request.

Credential files are read whenever a new token is acquired, and surrounding whitespace is trimmed from their contents. Missing, unreadable, or empty files cause token acquisition to fail. Credentials rotated at the configured paths take effect when the cached token is next replaced.

Prefer file-based credentials, especially for the client secret. In Kubernetes, mount them from a Secret. Literal credentials supplied through the Helm chart’s `config.content` appear in the rendered ConfigMap and in `helm get values`.

## Metrics

Metrics are exported when `service.metrics` is configured. The collector uses a dedicated Prometheus registry and serves it at `/metrics`.

The table lists OpenTelemetry instrument names and attributes. The Prometheus exporter replaces dots with underscores, adds `_total` to counter names, and adds unit suffixes where applicable. For example, `source.id` becomes the label `source_id`, and duration metrics use the suffix `_seconds`. Histograms expose `_bucket`, `_sum`, and `_count` series.

The listed metrics use the prefix `flightctl_catalogcollector_`. Additional exporter metadata metrics may also appear.

| Instrument | Kind | Attributes | Description |
|---|---|---|---|
| `flightctl.catalogcollector.source.snapshots` | Counter | `source.id` | Snapshots received from a source, before delivery to its pipelines. |
| `flightctl.catalogcollector.source.snapshot.resources` | Histogram | `source.id`, `resource.kind` | Resources per snapshot received from a source. |
| `flightctl.catalogcollector.source.collections` | Counter | `source.id`, `outcome` | Model Registry collection attempts, including attempts that fail before producing a snapshot. |
| `flightctl.catalogcollector.source.collection.duration` | Histogram | `source.id`, `outcome` | Duration of a Model Registry collection attempt, in seconds. Excludes pipeline processing. |
| `flightctl.catalogcollector.source.last_success.timestamp` | Gauge | `source.id` | Unix timestamp of the last successful Model Registry collection, in seconds. Not reported until the first successful collection. |
| `flightctl.catalogcollector.pipeline.syncs` | Counter | `pipeline.id`, `destination.id`, `outcome` | Pipeline synchronization attempts. |
| `flightctl.catalogcollector.pipeline.sync.duration` | Histogram | `pipeline.id`, `destination.id`, `outcome` | Duration of pipeline processing, including processors, snapshot validation, and the destination, in seconds. |
| `flightctl.catalogcollector.pipeline.snapshot.resources` | Histogram | `pipeline.id`, `destination.id`, `outcome`, `resource.kind` | Resources per snapshot entering a pipeline, before processors run. |

Snapshot and pipeline metrics apply to all sources. Collection metrics and the last-success timestamp are emitted by the `kubeflowmodelregistry` source.

Collection metrics are recorded before the snapshot enters its pipelines. A successful collection advances the last-success timestamp even if a processor, snapshot validation, or destination subsequently fails. Use pipeline metrics to monitor synchronization outcomes. Startup preflight checks are not counted as collection or synchronization attempts.

The `outcome` attribute is `success`, `cancelled`, or `failure`:

- Collection metrics classify context cancellation as `cancelled` and request or collection timeouts as `failure`.
- Pipeline metrics classify context cancellation and deadline expiration as `cancelled`. Other errors are classified as `failure`.

The `resource.kind` attribute is `Catalog` or `CatalogItem`. Collector-defined attribute values are limited to configured component and pipeline identifiers and fixed resource-kind and outcome values. Resource names, revisions, URLs, and error messages are not recorded as metric attributes.

Scrape the running collector to inspect its exported series:

```console
curl -fsS http://127.0.0.1:8888/metrics | grep '^flightctl_catalogcollector_'
```

## Further reading

* [Catalog collector overview](../using/catalog-collector/overview.md)
* [Installing the catalog collector](../installing/installing-catalog-collector.md)
* [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md)
* [Extending the catalog collector](../../developer/extending-catalog-collector.md)
