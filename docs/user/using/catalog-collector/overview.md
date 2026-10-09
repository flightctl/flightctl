# Catalog collector overview

The Flight Control catalog collector is a standalone service that imports software catalogs from an external system of record into Flight Control. Use it when the authoritative list of deployable content lives somewhere other than Flight Control, for example in a model registry, an artifact repository, or an internal product catalog.

> [!NOTE]
> The software catalog is an alpha-stage feature available under the `v1alpha1` API version. The catalog collector and its configuration may change in future releases.

## What the collector does

The collector runs outside the Flight Control service as its own container or systemd unit. On every cycle it performs the following work:

1. A *source* obtains the complete desired catalog content from an external system.
2. Zero or more *processors* transform that content, for example by renaming a catalog.
3. A *destination* reconciles the result into the target system, usually the Flight Control API.

The unit of work is a **snapshot**: the complete desired state for the scope that the source covers, observed at one point in time. A source never emits a partial snapshot. If a fetch, a pagination step, or a normalization step fails, the source reports the error and the cycle produces nothing.

The Flight Control destination treats a successful snapshot as authoritative for the pipeline that produced it. It creates or updates the resources in the snapshot, leaves unchanged resources untouched, and deletes resources that the same pipeline created earlier but that the snapshot no longer contains. An empty snapshot is a valid desired state that removes everything the pipeline manages.

## Choosing between the collector and ResourceSync

Flight Control offers two ways to populate a catalog from outside the API. They are independent, and a single deployment can use both for different catalogs.

| | Catalog collector | [ResourceSync](../managing-catalogs.md#importing-catalogs-using-resourcesync) |
|---|---|---|
| Source of truth | An external system with its own API, such as a model registry | A Git repository holding Catalog and CatalogItem YAML files |
| Who runs the import | A separate collector process that you deploy and operate | The Flight Control service itself |
| Direction | The collector pushes to the Flight Control API | The service pulls from Git |
| Resource ownership | Resources carry collector labels. The collector manages only resources inside its own boundary, but other authorized API clients can still edit them. | Resources are marked as owned by the ResourceSync and are not editable through the API, CLI, or UI |
| Typical use | Mirroring a registry that already publishes versioned content | Managing hand-authored catalog definitions with review and history |

Use ResourceSync when you author catalog content yourself and want Git review and history. Use the collector when another system already owns the content and you want Flight Control to track it.

## Configuration model

The collector reads one YAML file, supplied with `--config`. The file declares named component instances and then wires them into pipelines:

```yaml
service:        # process-wide settings: log level, metrics endpoint
extensions:     # shared capabilities: health endpoint, authenticators
sources:        # where catalog content comes from
processors:     # optional transformations
destinations:   # where catalog content goes
pipelines:      # source -> [processors] -> destination
```

The structure is deliberately modeled on the [OpenTelemetry Collector configuration](https://opentelemetry.io/docs/collector/configuration/): components are named with a `type[/name]` identifier. The component set is specific to Flight Control catalogs. No OpenTelemetry Collector component, receiver, or exporter is compatible with it.

Two different activation rules apply:

* **Sources, processors, and destinations** are built only when a pipeline references them. A declaration that no pipeline uses is ignored, and its type does not need a registered factory.
* **Extensions** are always built. Every declared extension is constructed and started, whether or not a component references it, because an extension is a shared service capability rather than a pipeline stage.

### Component identifiers

Every key under `sources`, `processors`, `destinations`, and `extensions` is a component identifier in the form `type[/name]`:

* `http` selects the HTTP source type with no instance name.
* `http/snapshots` selects the same type with the instance name `snapshots`.

The type selects the implementation. The optional name distinguishes several instances of one type. Identifiers are unique within their own section, so a source named `http/input` and a destination named `http/input` do not collide.

### Pipelines

A pipeline names one source, an optional ordered list of processors, and one destination:

```yaml
pipelines:
  external-catalog:
    source: http/snapshots
    processors:
      - catalogname/rename
    destination: flightctl/service
```

The configuration must define at least one pipeline, and every component a pipeline references must exist. Several pipelines may share one source, in which case each snapshot is delivered to every branch. Several pipelines may also share one destination; the destination keeps each pipeline's resources separate and prunes only its own.

### Environment variables

Any string value in a component configuration may be replaced by an environment variable reference, written as `${env:NAME}` or `${NAME}`. The reference must occupy the whole value; interpolation inside a longer string is not supported. An unset variable stops startup with an error that names the variable and its location in the configuration.

For the complete list of fields, defaults, and validation rules, see the [catalog collector configuration reference](../../references/catalog-collector.md).

## Integration paths

There are two supported ways to bring an external system into the collector.

### HTTP adapter

Run the built-in `http` source and have your own adapter POST complete snapshots to it. The adapter owns the integration: it queries the external system, converts the result into Catalog and CatalogItem resources, and sends them as one JSON document. The collector then validates, processes, and reconciles that snapshot.

Choose this path when:

* The external system has a bespoke or proprietary interface.
* The conversion logic belongs to a team that does not work in the Flight Control repository.
* You already have a job or controller that can produce the content.

The HTTP source has no inbound authentication. Bind it to loopback or keep it inside a cluster network, and never expose it through an Ingress, a Route, or a published routable port.

### Native source

Compile a source component into a collector binary. A native source polls the external system directly inside the collector process and emits snapshots in memory, so there is no adapter to deploy, no second credential path, and no HTTP hop.

Choose this path when:

* The external system has a stable API that many deployments share.
* You want polling, bounded backoff, preflight validation, and metrics handled by the collector framework.

The shipped `kubeflowmodelregistry` source is a native source. See [Importing a Kubeflow Model Registry](kubeflow-model-registry.md) for its workflow, and the [custom catalog collector sources](../../../developer/catalog-collector-sources.md) developer guide for how to write your own.

## Snapshot format

The HTTP source accepts a single JSON object with three required fields:

| Field | Type | Description |
|---|---|---|
| `revision` | string | An opaque, deterministic identifier of the desired content. It must be non-empty, and it must change only when the content changes. |
| `catalogs` | array | Desired Catalog resources. Send `[]` for an empty collection; the field may not be omitted. |
| `catalogItems` | array | Desired CatalogItem resources. Send `[]` for an empty collection; the field may not be omitted. |

Unknown fields are rejected, both at the top level and inside each resource. The maximum accepted body size is 16 MiB. A snapshot that the collector accepts and delivers returns `204 No Content`. A malformed snapshot returns `400 Bad Request`, and a failure further down the pipeline returns `502 Bad Gateway`.

Each element of `catalogs` and `catalogItems` is a Flight Control resource in the same shape that `flightctl apply` accepts. The fields are described in [Software catalog](../managing-catalogs.md). The following snapshot is complete and valid:

```json
{
  "revision": "2026-01-15T09:30:00Z",
  "catalogs": [
    {
      "apiVersion": "flightctl.io/v1alpha1",
      "kind": "Catalog",
      "metadata": {
        "name": "ai-models",
        "labels": {
          "source": "example-registry"
        }
      },
      "spec": {
        "displayName": "AI Models",
        "shortDescription": "Models imported from the example registry"
      }
    }
  ],
  "catalogItems": [
    {
      "apiVersion": "flightctl.io/v1alpha1",
      "kind": "CatalogItem",
      "metadata": {
        "name": "object-detector",
        "catalog": "ai-models"
      },
      "spec": {
        "type": "data",
        "category": "application",
        "displayName": "Object Detector",
        "shortDescription": "Vision model for edge inference",
        "provider": "example-team",
        "artifacts": [
          {
            "type": "container",
            "uri": "quay.io/example/object-detector"
          }
        ],
        "versions": [
          {
            "version": "1.0.0",
            "channels": ["stable"],
            "references": {
              "container": "sha256:0f3a1ad6b4a2c5f2b2c8ef7e4d5a6b7c8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a"
            }
          },
          {
            "version": "1.1.0",
            "channels": ["stable"],
            "replaces": "1.0.0",
            "references": {
              "container": "sha256:1b2c3d4e5f60718293a4b5c6d7e8f90112233445566778899aabbccddeeff001"
            }
          }
        ]
      }
    }
  ]
}
```

Before a snapshot reaches a destination, the collector validates it: the revision must be non-empty, every resource must pass Flight Control API validation, Catalog names must be unique, and each CatalogItem must have both a catalog and a name that together are unique.

## Quickstart

This procedure wires an HTTP source to a Flight Control destination, posts a snapshot into the collector, and confirms that the catalog arrives in Flight Control. It then posts a second, changed snapshot and confirms that the update is applied.

The procedure uses only the collector binary, the `curl` command, and the Flight Control CLI. It makes no assumption about where the collector runs: a Linux host, a container, and a pod all behave the same, because the collector reads one configuration file and two credential paths. Only those paths change between environments.

### Prerequisites

* A reachable Flight Control API endpoint, and an account that may create and update Catalog and CatalogItem resources.
* An API token that the Flight Control service accepts. How you obtain one depends on the authentication method your deployment uses; see [Authentication overview](../../installing/configuring-auth/overview.md).
* The CA bundle that signs the Flight Control API certificate, unless that certificate is already trusted by the system trust store.
* A collector binary, for example from `make build-catalog-collector` in a clone of the Flight Control repository, or the collector container image.
* The `curl` command and the Flight Control CLI. See [Installing the Flight Control CLI](../../installing/installing-cli.md).

### Procedure

1. Create a working directory for the credential and the CA bundle:

    ```console
    mkdir -p -m 0700 ~/catalog-collector
    ```

2. Write the API token into a file that only you can read. The collector reads this file, never an environment variable or a command output:

    ```console
    install -m 0600 /dev/null ~/catalog-collector/flightctl-token
    ```

    ```console
    printf '%s' '<api_token>' > ~/catalog-collector/flightctl-token
    ```

3. Copy the CA bundle that signs the Flight Control API certificate next to the token:

    ```console
    cp <path_to_ca_bundle> ~/catalog-collector/flightctl-ca.crt
    ```

    Omit this step and the `certificateAuthority` field in the next step when the API certificate is signed by a certificate in the system trust store.

4. Save the following configuration as `~/catalog-collector/collector.yaml`, replacing `<flightctl_api_endpoint>` with the base URL of your Flight Control API:

    ```yaml
    service:
      logLevel: info
      metrics:
        endpoint: 127.0.0.1:8888

    extensions:
      healthcheck:
        endpoint: 127.0.0.1:13133
        livePath: /livez
        readyPath: /readyz

      bearertokenauth/flightctl:
        tokenFile: /home/<user_name>/catalog-collector/flightctl-token

    sources:
      http/snapshots:
        listenAddress: 127.0.0.1:8080
        path: /v1/snapshots

    destinations:
      flightctl/service:
        server: https://<flightctl_api_endpoint>
        certificateAuthority: /home/<user_name>/catalog-collector/flightctl-ca.crt
        timeout: 30s
        auth:
          authenticator: bearertokenauth/flightctl

    pipelines:
      external-catalog:
        source: http/snapshots
        destination: flightctl/service
    ```

    Every path in the file is read by the collector process. In a container or a pod, use the paths the files are mounted at, not the paths they occupy on the host.

5. Start the collector:

    ```console
    bin/flightctl-catalog-collector --config ~/catalog-collector/collector.yaml
    ```

6. In a second terminal, confirm that the collector is ready:

    ```console
    curl -sS -o /dev/null -w '%{http_code}\n' http://127.0.0.1:13133/readyz
    ```

    The expected output is:

    ```text
    200
    ```

7. Save the example snapshot from [Snapshot format](#snapshot-format) as `snapshot.json` and post it:

    ```console
    curl -sS -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8080/v1/snapshots \
        -H 'Content-Type: application/json' \
        --data-binary @snapshot.json
    ```

    The expected output is:

    ```text
    204
    ```

    A `502` means the snapshot was well formed but the pipeline failed to reconcile it. Read the collector log, which names the failing stage.

8. Confirm with the CLI that the catalog arrived:

    ```console
    flightctl get catalogs
    ```

    ```console
    flightctl get catalogitems --catalog ai-models
    ```

9. Confirm that the collector labeled what it wrote:

    ```console
    flightctl get catalogs -l flightctl.io/catalog-collector-pipeline=external-catalog
    ```

10. Change the snapshot and post it again. Edit `snapshot.json`, set `revision` to a new value such as `2026-01-15T10:00:00Z`, and change `spec.shortDescription` of the catalog item to `Vision model for edge inference, revised`. Then post the file again:

    ```console
    curl -sS -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8080/v1/snapshots \
        -H 'Content-Type: application/json' \
        --data-binary @snapshot.json
    ```

11. Confirm that the update was applied:

    ```console
    flightctl get catalogitem object-detector --catalog ai-models -o yaml
    ```

    The `spec.shortDescription` field now carries the revised text. Removing the catalog item from the snapshot entirely and posting again deletes it, because the snapshot is the complete desired state for the pipeline.

### Using OAuth2 instead of a static token

A token file is the shortest path to a working pipeline, but a long-lived token is a poor production credential. The `oauth2client` extension exchanges client credentials for short-lived access tokens instead. Replace the `bearertokenauth/flightctl` extension with an `oauth2client/flightctl` extension and point `auth.authenticator` at it. See [Authenticating to Flight Control](../../installing/installing-catalog-collector.md#authenticating-to-flight-control) for the procedure and [oauth2client](../../references/catalog-collector.md#oauth2client) for every field.

## Ownership and pruning

The Flight Control destination labels every resource it writes:

| Label | Value |
|---|---|
| `flightctl.io/managed-by` | `flightctl-catalog-collector` |
| `flightctl.io/catalog-collector-pipeline` | The pipeline name from the configuration |

These labels define the boundary that the collector itself respects:

* A resource that already exists without both labels, or with a different pipeline name, is never adopted or overwritten. A desired name that collides with such a resource fails reconciliation.
* A resource that reports a `metadata.owner`, such as one created by a ResourceSync, is never modified.
* Pruning considers only resources carrying both labels with the matching pipeline name.
* A snapshot may not set either label itself. Reconciliation fails if it does.

Pruning runs only after every desired write has succeeded and both complete lists of managed resources have been retrieved, so a partial failure cannot delete live content. CatalogItems are deleted before their Catalogs.

Because the pipeline name is part of a label value, renaming a pipeline makes the new pipeline lose sight of the resources the old name created. Delete the stale resources, or keep the pipeline name stable.

### What the labels do not do

The labels are a boundary the collector applies to itself. They are not an access control.

* Any other client that is authorized to call the Flight Control API can still create, edit, and delete a resource carrying the collector labels. The API, the CLI, and the UI treat it like any other Catalog or CatalogItem.
* A manual edit to a resource the collector manages is not permanent. The collector compares each desired resource against the live one on every synchronization and rewrites it when the specification or the labels differ, so the next successful snapshot restores the collector's view.
* Removing the labels by hand does not transfer the resource. It takes the resource outside the pipeline boundary, so the collector stops pruning it and the next snapshot that wants the same name fails instead of overwriting it.

ResourceSync uses a different mechanism. It sets `metadata.owner` on the resources it creates, and the Flight Control API refuses edits to an owned resource. If you need a catalog that cannot be edited outside its import path, use ResourceSync. See [Importing catalogs using ResourceSync](../managing-catalogs.md#importing-catalogs-using-resourcesync).

## Operating the collector

### Startup sequence

Startup is ordered so that a configuration error fails before any catalog content is written:

1. The metrics endpoint binds, if `service.metrics` is configured.
2. Every declared extension is constructed and started.
3. Each source that implements the optional preflight capability validates its own external dependencies.
4. Source goroutines start.
5. Extensions that take part in the readiness lifecycle, such as `healthcheck`, report ready.

Readiness proves less than it may appear to:

* **Preflight is optional.** It is a capability a source may implement, not a stage every source runs. The built-in `http` source implements no preflight, so a pipeline built on it reports ready without contacting anything.
* **Preflight covers the source, not the destination.** The `kubeflowmodelregistry` source queries the Model Registry once and fails fast if the query is rejected, which does exercise its registry credential. No preflight runs against a destination, so a ready collector has not proved that its Flight Control credential is accepted.
* **Readiness says nothing about later cycles.** Collection and reconciliation both happen after startup and either can fail on its own.

Use the metrics and the log to confirm that content is flowing, and the CLI to confirm that it arrived.

### Health endpoints

The `healthcheck` extension serves a liveness path and a readiness path. Liveness answers `200 OK` whenever the HTTP server is running. Readiness answers `200 OK` only after startup completes, and reverts to a failure response during shutdown.

### Metrics

When `service.metrics` is present, the collector serves a Prometheus endpoint at the fixed path `/metrics` on the configured address. The path is not configurable. The exported series are prefixed `flightctl_catalogcollector_` and cover collection attempts, pipeline syncs, their durations, and the resource counts per snapshot.

### Applying configuration changes

The collector reads its configuration once, at startup, and has no reload signal. Restart it after editing the configuration file.

Credential files are re-read without a restart, but not all on the same schedule:

| Field | When the collector reads the file |
|---|---|
| `bearertokenauth.tokenFile` | Before every outgoing request. A token replaced in place is used by the next request. |
| `oauth2client.clientIdFile` and `oauth2client.clientSecretFile` | Only when a new access token is acquired: on the first request, and whenever the cached token is within `expiryBuffer` of its expiry. |

An `oauth2client` extension therefore keeps using its cached access token after you replace the client credentials on disk. The new credentials take effect when that token is next replaced. Restart the collector when a rotation must take effect immediately.

## Troubleshooting

### Inspecting what a source produces

The `debug` destination logs snapshots instead of writing them anywhere. Point a pipeline at it to separate a source or adapter problem from a destination problem, because a pipeline that ends in `debug` contacts no Flight Control service and needs no credential:

```yaml
destinations:
  debug/stdout:
    verbosity: normal

pipelines:
  external-catalog:
    source: http/snapshots
    destination: debug/stdout
```

Set `verbosity` to `basic` for the revision and resource counts, `normal` to add the resource names, or `detailed` to log the full JSON body of every resource. Enable `detailed` deliberately: catalog metadata may contain sensitive operational information.

### Interpreting the HTTP source response

| Response | Meaning |
|---|---|
| `204 No Content` | The snapshot was accepted, validated, and reconciled by every pipeline that uses this source. |
| `400 Bad Request` | The body is not a valid snapshot. The response text names the problem. |
| `415 Unsupported Media Type` | The request did not set `Content-Type: application/json`. |
| `502 Bad Gateway` | The snapshot was well formed, but a processor or a destination rejected it. The collector log names the failing stage. |

### Common failures

| Symptom | Likely cause |
|---|---|
| Startup fails naming a configuration path | A misspelled field, an unknown field, or an unset environment variable. Decoding is strict at every level. |
| Startup fails naming an extension | A component references an authenticator identifier that no extension declares, or the extension does not provide the capability the component needs. |
| Reconciliation fails with a name collision | A resource with that name already exists outside the pipeline boundary. See [What the labels do not do](#what-the-labels-do-not-do). |
| The collector is ready, but nothing reaches Flight Control | Readiness does not exercise the destination credential. Check the pipeline sync metrics and the log. |
| Resources vanish after a pipeline rename | The renamed pipeline writes a new label value and no longer sees the old resources. Delete them by label, or keep the name stable. |

## Further reading

* [Installing the catalog collector](../../installing/installing-catalog-collector.md)
* [Importing a Kubeflow Model Registry](kubeflow-model-registry.md)
* [Catalog collector configuration reference](../../references/catalog-collector.md)
* [Software catalog](../managing-catalogs.md)
* [Writing custom catalog collector sources](../../../developer/catalog-collector-sources.md)
