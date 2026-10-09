# Catalog collector configuration reference

This reference describes every field of the Flight Control catalog collector configuration file and every component shipped with the official collector distribution. For concepts and workflows, see [Catalog collector overview](../using/catalog-collector/overview.md).

The collector takes exactly one argument:

```console
flightctl-catalog-collector --config <path>
```

The file is YAML, and decoding is strict: an unknown key, a misspelled component field, or a stray top-level section stops startup with an error that names the location. Strictness is applied in two passes. The top-level sections, the `service` block, and the `pipelines` entries are checked when the file is parsed. The fields of a source, a processor, a destination, or an extension are checked when that component is constructed, so a declaration that is never constructed is never checked. See [Top-level sections](#top-level-sections) for which declarations are constructed.

## Top-level sections

| Section | Required | Description |
|---|---|---|
| `service` | No | Process-wide settings: log level and the internal metrics endpoint. |
| `extensions` | No | Shared capabilities that are not pipeline stages, such as the health endpoint and authenticators. |
| `sources` | Yes, in practice | Named source instances. At least one is required because every pipeline references one. |
| `processors` | No | Named processor instances. |
| `destinations` | Yes, in practice | Named destination instances. At least one is required because every pipeline references one. |
| `pipelines` | Yes | Named pipelines. At least one pipeline must be defined. |

Declaration and activation differ by component kind. A source, a processor, or a destination is constructed only when a pipeline references it. A declaration that no pipeline references is ignored, and its fields are neither strictly decoded nor resolved for environment variable references. Every declared extension is constructed and started, whether or not a component references it, so the fields of every declared extension are always checked.

## Component identifiers

Each key under `sources`, `processors`, `destinations`, and `extensions` is a component identifier of the form `type[/name]`.

| Rule | Example |
|---|---|
| Type only | `http` |
| Type and instance name | `http/snapshots` |
| An empty identifier is rejected | `""` |
| An empty type is rejected | `/snapshots` |
| An empty name after the separator is rejected | `http/` |
| More than one separator is rejected | `http/a/b` |

Identifiers are unique within their own section. The same identifier may appear as a source and as a destination without conflict.

## Environment variable references

Any string value inside a component configuration may be replaced with an environment variable reference. Only the bodies under `sources`, `processors`, `destinations`, and `extensions` are expanded; a reference written under `service` or `pipelines` is kept as literal text.

```yaml
destinations:
  flightctl/service:
    server: ${env:FLIGHTCTL_SERVER}
```

| Rule | Behavior |
|---|---|
| Accepted forms | `${env:NAME}` and `${NAME}` |
| Placement | The reference must be the entire value. Inline interpolation such as `https://${HOST}/api` is not supported. |
| Object keys | Never expanded. |
| Malformed reference | A value wrapped in `${...}` that matches neither form is an error, not a literal. |
| Unset variable | A fatal startup error naming the variable and its configuration path. The value is never logged. |

References are resolved when the component is decoded, not when the file is parsed. A declaration that is never constructed is never decoded, so an unset variable inside it is never reported.

## service

```yaml
service:
  logLevel: info
  metrics:
    endpoint: 127.0.0.1:8888
```

| Field | Type | Default | Description |
|---|---|---|---|
| `logLevel` | string | Unchanged | One of `debug`, `info`, `warn`, or `error`. Omit the field to keep the level the process started with. An explicit empty string is rejected. |
| `metrics` | object | Absent | Prometheus metrics endpoint. Omit the whole block to disable metrics. |
| `metrics.endpoint` | string | `localhost:8888` | A `host:port` pair. Both parts are required. The metrics path is fixed at `/metrics` and is not configurable. |

In a container, set `metrics.endpoint` to `0.0.0.0:8888`. The default binds loopback inside the container only and is not reachable from a published port or from Prometheus.

## pipelines

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
| `source` | string | Yes | Identifier of a declared source. |
| `processors` | list of strings | No | Identifiers of declared processors, applied in the listed order. |
| `destination` | string | Yes | Identifier of a declared destination. |

Validation rejects a configuration with no pipelines, a pipeline that omits `source` or `destination`, and any reference to a component that is not declared.

Several pipelines may reference the same source. The snapshot is then delivered to every pipeline, and each pipeline reconciles independently. Several pipelines may also reference the same destination. The destination serializes reconciliations per pipeline and keeps the managed resources of each pipeline separate.

The pipeline name is written into a label on every resource the Flight Control destination creates, so it must be valid as a Kubernetes label value.

## Sources

### http

Accepts complete snapshots over HTTP. Intended for adapters that convert an external system into Flight Control resources, and for local testing.

```yaml
sources:
  http/snapshots:
    listenAddress: 127.0.0.1:8080
    path: /v1/snapshots
```

| Field | Type | Default | Description |
|---|---|---|---|
| `listenAddress` | string | `127.0.0.1:8080` | Address the HTTP server binds. Must not be empty. |
| `path` | string | `/v1/snapshots` | Absolute path that accepts snapshots. Must start with `/` and must not contain `{` or `}`. |

Request handling:

| Condition | Response |
|---|---|
| Method other than `POST` | `405 Method Not Allowed` |
| `Content-Type` other than `application/json` | `415 Unsupported Media Type` |
| Body larger than 16 MiB | `400 Bad Request` |
| Invalid JSON, or trailing data after the object | `400 Bad Request` |
| Unknown field, at the top level or anywhere inside a resource | `400 Bad Request` |
| Missing or empty `revision` | `400 Bad Request` |
| `catalogs` or `catalogItems` absent, or set to the JSON literal `null` | `400 Bad Request` |
| `catalogs` or `catalogItems` set to `[]` | Accepted. An empty array is a valid, complete snapshot of an empty desired state, and is not treated as a missing field. |
| Snapshot validation, a processor, or a destination failed | `502 Bad Gateway` |
| Every pipeline using this source reported success | `204 No Content` |

The handler checks only the envelope. Snapshot validation runs inside the pipeline, so a body whose resources fail Flight Control API validation is answered with `502`, not `400`. Equally, `204` reports that every pipeline using this source returned success; with a `debug` destination that means the snapshot was logged, not that anything was written to Flight Control.

> [!WARNING]
> The HTTP source has no built-in inbound authentication, so anyone who can reach it can replace or delete the catalog content the pipeline manages. Bind it to loopback when you run the collector locally. Anywhere else, restrict access to the clients that have to post snapshots; a cluster-internal address alone does not authenticate callers. When the endpoint has to be reachable beyond that, put an authenticating proxy in front of it and protect the transport and the network path.

### kubeflowmodelregistry

Polls a Kubeflow Model Registry, as exposed by Red Hat OpenShift AI, and converts registered models into catalog items. See [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md).

```yaml
sources:
  kubeflowmodelregistry/rhoai:
    endpoint: https://model-registry.example.com
    catalog: rhoai-model-registry
    pollInterval: 5m
    auth:
      authenticator: bearertokenauth/model-registry
```

| Field | Type | Default | Description |
|---|---|---|---|
| `endpoint` | string | None | Required. Absolute `http` or `https` base URL of the Model Registry REST API. The value may not contain user information, query parameters, a `#` character, or surrounding whitespace. |
| `catalog` | string | None | Required. Name of the Flight Control Catalog this source populates. Must be a valid Flight Control resource name. |
| `pollInterval` | duration | `5m` | Wait between successive successful collection cycles. Must be positive. |
| `pageSize` | integer | `100` | Items requested per paginated API call. Must be between 0 and 2147483647; `0` selects the default. |
| `requestTimeout` | duration | `30s` | Bounds a single HTTP request. Must be positive. |
| `collectionTimeout` | duration | `5m` | Bounds one complete collection cycle, including pagination and normalization. Must be positive. |
| `selection.modelFilter` | string | `state='LIVE'` | Value sent as `filterQuery` to the registered-models endpoint. Set to `""` to send no filter and import every model. |
| `selection.versionFilter` | string | `state='LIVE'` | Value sent as `filterQuery` to the model-versions endpoint. Set to `""` to send no filter. |
| `auth.authenticator` | string | None | Identifier of an extension that authenticates outgoing requests, for example `bearertokenauth/model-registry`. Requires an `https` endpoint. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle used to verify the registry certificate. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS verification. Development only; the collector logs a warning at startup when it is enabled. |
| `backoff.initialInterval` | duration | `1s` | Base delay after the first failed cycle. Must be positive. |
| `backoff.maxInterval` | duration | `5m` | Upper bound on the retry delay, including jitter. Must be positive and not smaller than `initialInterval`. |
| `backoff.multiplier` | number | `2` | Factor applied to the base interval after each consecutive failure. Must be finite and at least `1.0`. |
| `backoff.randomizationFactor` | number | `0.5` | Symmetric jitter around the base interval. Must be between 0 and 1 inclusive. |

The source implements the optional preflight capability. During startup it issues one query against each configured filter and fails before the collector reports ready if the registry rejects it. The check covers the registry endpoint and this source's credential only; it says nothing about the destination the pipeline writes to.

## Processors

### catalogname

Renames Catalog resources and updates the `metadata.catalog` field of the matching CatalogItem resources so items stay attached to the renamed catalog.

```yaml
processors:
  catalogname/rename:
    mappings:
      rhoai-model-registry: ai-models
```

| Field | Type | Default | Description |
|---|---|---|---|
| `mappings` | map of string to string | None | Required and not empty. Each key is an existing catalog name and each value is its replacement. Keys and values must be non-empty, and two keys may not map to the same value. |

A catalog name that is not listed passes through unchanged. If a rename would produce two catalogs with the same name, or two catalog items with the same catalog and name, the snapshot is rejected and the cycle fails.

## Destinations

### flightctl

Reconciles snapshots into a Flight Control service.

```yaml
destinations:
  flightctl/service:
    server: https://flightctl.example.com
    certificateAuthority: /etc/flightctl/catalog-collector/certs/flightctl-ca.crt
    timeout: 30s
    auth:
      authenticator: oauth2client/flightctl
```

| Field | Type | Default | Description |
|---|---|---|---|
| `server` | string | None | Required. Base URL of the Flight Control API. A trailing slash is removed before the API path is appended. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle. The file must contain at least one valid PEM certificate. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS verification. Development only. |
| `orgId` | string | Service default | Target organization. Must be a valid UUID when set. |
| `auth.authenticator` | string | None | Identifier of an extension that authenticates outgoing requests. When omitted, requests are unauthenticated. The identifier must not be empty when `auth` is present. |
| `timeout` | duration | `30s` | Per-request timeout for API calls. Must be positive when set. |

Connections negotiate a minimum of TLS 1.3, and proxy settings are taken from the standard proxy environment variables.

The destination labels every resource it manages:

| Label | Value |
|---|---|
| `flightctl.io/managed-by` | `flightctl-catalog-collector` |
| `flightctl.io/catalog-collector-pipeline` | The pipeline name |

Reconciliation behavior:

* Resources in the snapshot are created or updated. A resource that already matches the desired specification and labels is left untouched.
* Resources carrying both labels for this pipeline that the snapshot does not contain are deleted. CatalogItems are deleted before Catalogs.
* Pruning starts only after all writes succeed and the complete lists of managed Catalogs and CatalogItems have been retrieved. Once it starts, the deletions are issued one at a time and are not rolled back: a failed deletion abandons the rest and leaves the earlier ones applied.
* An existing resource without the collector labels is never adopted. A desired name that collides with such a resource fails the reconciliation.
* A resource that reports an owner is never modified.
* A snapshot that sets either reserved label itself is rejected. This check belongs to this destination. The shared snapshot validation that runs before every destination does not inspect labels.
* Reconciliation is not transactional. A failure part-way through leaves earlier writes committed. Pruning is ordered last so that a failure before it deletes nothing, but it is not atomic in itself.

The destination does not decide what may be deleted. It issues the request and the API answers:

* The API rejects the deletion of a CatalogItem while a device or a fleet references one of its versions, with a conflict naming those versions. A snapshot that omits the item therefore fails to prune it until the references are removed. The same rule rejects a write that removes or alters an in-use version.
* The API rejects a specification change or a deletion on a resource that reports an owner. The destination refuses to touch owned resources before it reaches that point.

The credential the destination uses must be allowed to read and list, create and update, and delete both Catalog and CatalogItem resources. Pruning is part of normal reconciliation, so a credential without delete permission fails every cycle in which something has to be removed.

The labels bound what the collector manages. They are not an access control: any other client authorized to call the Flight Control API can edit or delete a labeled resource, and the next successful synchronization overwrites a manual edit. For a catalog whose content the API itself protects, use ResourceSync, which sets `metadata.owner`.

### debug

Logs snapshots instead of writing them anywhere. Use it to inspect what a source or an adapter produces.

```yaml
destinations:
  debug/stdout:
    verbosity: normal
```

| Field | Type | Default | Description |
|---|---|---|---|
| `verbosity` | string | `basic` | One of `basic`, `normal`, or `detailed`. |

| Value | Logged content |
|---|---|
| `basic` | Pipeline identifier, revision, and resource counts. |
| `normal` | The basic summary plus sorted catalog names and catalog item identities. |
| `detailed` | The basic summary plus one entry per resource, including the full JSON body. Enable deliberately: catalog metadata may contain sensitive operational information. |

## Extensions

### healthcheck

Serves liveness and readiness endpoints.

```yaml
extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz
```

| Field | Type | Default | Description |
|---|---|---|---|
| `endpoint` | string | `localhost:13133` | A `host:port` pair. Both parts are required. |
| `livePath` | string | `/livez` | Liveness path. |
| `readyPath` | string | `/readyz` | Readiness path. |

Both paths must start with `/`, must be canonical, and must not contain a query string, a fragment, percent-encoded characters, or the `{` and `}` characters. The two paths must differ.

Liveness answers `200 OK` while the server is running. Readiness answers `200 OK` only after every source goroutine has started, and stops doing so when shutdown begins.

In a container, bind the endpoint to `0.0.0.0`. The default is reachable only from inside the container network namespace.

### bearertokenauth

Adds an `Authorization: Bearer` header to outgoing requests of the component that references it.

```yaml
extensions:
  bearertokenauth/model-registry:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
```

| Field | Type | Default | Description |
|---|---|---|---|
| `token` | string | None | Literal token value. Mutually exclusive with `tokenFile`. |
| `tokenFile` | string | None | Path to a file containing the token. Mutually exclusive with `token`. |

Exactly one of the two must be set. The token value is never logged.

When `tokenFile` is used, the file is read on every request and surrounding whitespace is trimmed, so a credential rotated in place takes effect without a restart. An empty token or an empty token file fails the request.

### oauth2client

Obtains an access token with the OAuth 2.0 client-credentials grant and attaches it to outgoing requests. Prefer this extension over a long-lived static token.

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
| `tokenUrl` | string | None | Required. Absolute `http` or `https` token endpoint. It must not contain user information or a fragment. |
| `scopes` | list of strings | None | Scopes requested from the authorization server. No entry may be empty. |
| `endpointParams` | map of string to string | None | Extra parameters added to the token request. The names `client_id`, `client_secret`, `grant_type`, and `scope` are managed by the client and are rejected. |
| `certificateAuthority` | string | System roots | Path to a PEM CA bundle for the authorization server. |
| `insecureSkipVerify` | boolean | `false` | Disables TLS verification for the token request. Development only. |
| `timeout` | duration | `30s` | Timeout for the token request. Must be greater than zero. |
| `expiryBuffer` | duration | `10s` | Treats a cached token as expired this long before its real expiry. Must not be negative. |

The `tokenUrl` value in the example above is a Keycloak realm endpoint, because Keycloak is a common choice in Flight Control deployments. It is only an example. Take the token endpoint from your own authorization server, usually from the `token_endpoint` field of its OpenID Connect discovery document; the path differs between providers.

Tokens are fetched lazily. When a request is about to be sent and the cached token has less than `expiryBuffer` remaining, a replacement is obtained first. The buffer narrows the window in which a token expires in flight; it does not make a `401` response impossible.

`clientIdFile` and `clientSecretFile` are read at the moment a new token is acquired, not on every request. Credentials rotated on disk therefore take effect when the cached token is next replaced, which is a different schedule from the per-request read that `bearertokenauth.tokenFile` uses.

Prefer the file forms. Inline values appear in the configuration file and, in a Helm deployment, in the rendered ConfigMap and in `helm get values`.

## Metrics

Metrics are exported when `service.metrics` is present. The collector uses its own Prometheus registry and serves it at `/metrics`. Instrument names follow OpenTelemetry conventions and are translated by the Prometheus exporter, which replaces dots with underscores and appends a unit suffix, so every exported series starts with `flightctl_catalogcollector_`.

| Instrument | Kind | Attributes | Description |
|---|---|---|---|
| `flightctl.catalogcollector.source.snapshots` | Counter | `source.id` | Snapshots received from a source. |
| `flightctl.catalogcollector.source.snapshot.resources` | Histogram | `source.id`, `resource.kind` | Resources per snapshot received from a source. |
| `flightctl.catalogcollector.source.collections` | Counter | `source.id`, `outcome` | Collection attempts made by a polling source. |
| `flightctl.catalogcollector.source.collection.duration` | Histogram | `source.id`, `outcome` | Duration of a collection attempt, in seconds. |
| `flightctl.catalogcollector.source.last_success.timestamp` | Gauge | `source.id` | Unix timestamp of the last successful collection, in seconds. Not reported until the first success. |
| `flightctl.catalogcollector.pipeline.syncs` | Counter | `pipeline.id`, `destination.id`, `outcome` | Pipeline sync attempts. |
| `flightctl.catalogcollector.pipeline.sync.duration` | Histogram | `pipeline.id`, `destination.id`, `outcome` | Duration of a pipeline sync from the first processor to the destination, in seconds. |
| `flightctl.catalogcollector.pipeline.snapshot.resources` | Histogram | `pipeline.id`, `destination.id`, `outcome`, `resource.kind` | Resources per snapshot entering a pipeline, before processors run. |

The `outcome` attribute is `success`, `cancelled`, or `failure`. Only bounded, low-cardinality values are recorded as attributes. Resource names, revisions, URLs, and error messages are never used as attribute values.

Scrape the endpoint of the version you run to see the exact exported series:

```console
curl -s http://127.0.0.1:8888/metrics | grep flightctl_catalogcollector_
```

## Further reading

* [Catalog collector overview](../using/catalog-collector/overview.md)
* [Installing the catalog collector](../installing/installing-catalog-collector.md)
* [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md)
* [Writing custom catalog collector sources](../../developer/catalog-collector-sources.md)
