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
| Resource ownership | Resources carry collector labels and remain writable through the API only by the same pipeline | Resources are marked as managed by the ResourceSync and are not editable through the API, CLI, or UI |
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

The structure is deliberately modeled on the [OpenTelemetry Collector configuration](https://opentelemetry.io/docs/collector/configuration/): components are named with a `type[/name]` identifier, declaring a component does not activate it, and only the components referenced by a pipeline are built and started. The component set is specific to Flight Control catalogs. No OpenTelemetry Collector component, receiver, or exporter is compatible with it.

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

This procedure runs a collector locally, pushes one snapshot into it over HTTP, and prints the result. It needs no Flight Control service and no external registry, so it is the fastest way to confirm that the configuration model behaves as you expect.

### Prerequisites

* A built collector binary, for example from `make build-catalog-collector` in a clone of the Flight Control repository, or the collector container image.
* The `curl` command.

### Procedure

1. Save the following configuration as `collector.yaml`:

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

    sources:
      http/snapshots:
        listenAddress: 127.0.0.1:8080
        path: /v1/snapshots

    destinations:
      debug/stdout:
        verbosity: normal

    pipelines:
      snapshots:
        source: http/snapshots
        destination: debug/stdout
    ```

2. Start the collector:

    ```console
    bin/flightctl-catalog-collector --config collector.yaml
    ```

3. In a second terminal, confirm that the collector is ready:

    ```console
    curl -s http://127.0.0.1:13133/readyz
    ```

4. Push an empty snapshot:

    ```console
    curl -sS -o /dev/null -w '%{http_code}\n' -X POST http://127.0.0.1:8080/v1/snapshots \
        -H 'Content-Type: application/json' \
        -d '{"revision":"1","catalogs":[],"catalogItems":[]}'
    ```

    The expected output is:

    ```text
    204
    ```

5. Push the example snapshot from [Snapshot format](#snapshot-format), saved as `snapshot.json`:

    ```console
    curl -sS -X POST http://127.0.0.1:8080/v1/snapshots \
        -H 'Content-Type: application/json' \
        --data-binary @snapshot.json
    ```

    The collector logs the catalog and the catalog item it received.

### Writing to Flight Control

To send the same snapshots to a Flight Control service, replace the debug destination with the `flightctl` destination and attach an authenticator:

```yaml
extensions:
  healthcheck:
    endpoint: 127.0.0.1:13133

  bearertokenauth/flightctl:
    tokenFile: /etc/flightctl/catalog-collector/credentials/flightctl-token

sources:
  http/snapshots:
    listenAddress: 127.0.0.1:8080
    path: /v1/snapshots

destinations:
  flightctl/service:
    server: https://flightctl.example.com
    certificateAuthority: /etc/flightctl/catalog-collector/certs/flightctl-ca.crt
    timeout: 30s
    auth:
      authenticator: bearertokenauth/flightctl

pipelines:
  external-catalog:
    source: http/snapshots
    destination: flightctl/service
```

Verify the result with the CLI:

```console
flightctl get catalogs
flightctl get catalogitems --catalog ai-models
```

For production deployments prefer the `oauth2client` authenticator over a static token. See [Installing the catalog collector](../../installing/installing-catalog-collector.md).

## Ownership and pruning

The Flight Control destination labels every resource it writes:

| Label | Value |
|---|---|
| `flightctl.io/managed-by` | `flightctl-catalog-collector` |
| `flightctl.io/catalog-collector-pipeline` | The pipeline name from the configuration |

These labels define the reconciliation boundary:

* A resource that already exists without these labels is never adopted or overwritten. A desired name that collides with such a resource fails reconciliation.
* A resource owned by another actor, such as a ResourceSync, is never modified.
* Pruning considers only resources carrying both labels with the matching pipeline name.
* A snapshot may not set either label itself. Reconciliation fails if it does.

Pruning runs only after every desired write has succeeded and both complete lists of managed resources have been retrieved, so a partial failure cannot delete live content. CatalogItems are deleted before their Catalogs.

Because the pipeline name is part of a label value, renaming a pipeline makes the new pipeline lose sight of the resources the old name created. Delete the stale resources, or keep the pipeline name stable.

## Operating the collector

### Startup sequence

Startup is ordered so that a misconfiguration fails before any catalog content is written:

1. The metrics endpoint binds, if `service.metrics` is configured.
2. Extensions are constructed and started.
3. Sources that implement a preflight check validate their external dependencies. The `kubeflowmodelregistry` source queries the registry once and fails fast if the query is rejected.
4. Source goroutines start.
5. The health endpoint reports ready.

A collector that reports ready has therefore already validated its credentials against the upstream system. Readiness says nothing about the cycles that follow; use the metrics and the log to confirm that content is flowing.

### Health endpoints

The `healthcheck` extension serves a liveness path and a readiness path. Liveness answers `200 OK` whenever the HTTP server is running. Readiness answers `200 OK` only after startup completes, and reverts to a failure response during shutdown.

### Metrics

When `service.metrics` is present, the collector serves a Prometheus endpoint at the fixed path `/metrics` on the configured address. The path is not configurable. The exported series are prefixed `flightctl_catalogcollector_` and cover collection attempts, pipeline syncs, their durations, and the resource counts per snapshot.

### Applying configuration changes

The collector reads its configuration once, at startup, and has no reload signal. Restart it after editing the configuration file. Credential files referenced by `tokenFile`, `clientIdFile`, and `clientSecretFile` are re-read per request, so rotating a credential in place does not require a restart.

## Further reading

* [Installing the catalog collector](../../installing/installing-catalog-collector.md)
* [Importing a Kubeflow Model Registry](kubeflow-model-registry.md)
* [Catalog collector configuration reference](../../references/catalog-collector.md)
* [Software catalog](../managing-catalogs.md)
* [Writing custom catalog collector sources](../../../developer/catalog-collector-sources.md)
