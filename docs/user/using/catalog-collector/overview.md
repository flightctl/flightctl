# Catalog collector overview

The Flight Control catalog collector is a standalone service that imports catalog metadata into Flight Control from an external platform that already maintains it. It imports the descriptive record of each component, the versions that exist, and the artifact references those versions resolve to. It does not copy the artifacts themselves, which devices continue to pull from the registry that hosts them.

Use the collector when the authoritative list of deployable content lives somewhere other than Flight Control, for example in a model registry or an internal product catalog.

> [!NOTE]
> The software catalog is an alpha-stage feature. Catalog and CatalogItem resources are served under the `v1alpha1` API version, and the collector writes that version. The collector configuration file is a separate format: it is not covered by the API version, and its fields may change in any release.

## What the collector does

The collector runs outside the Flight Control service as its own container or systemd unit. Each cycle moves one snapshot through three stages:

1. A *source* obtains the complete desired catalog content from an external system.
2. Zero or more *processors* transform that content, for example by renaming a catalog.
3. A *destination* consumes the result. The `flightctl` destination reconciles it into a Flight Control service; the `debug` destination only logs it.

The unit of work is a **snapshot**: the complete set of Catalog and CatalogItem resources that the source's scope should contain, observed at one point in time, together with a revision identifier. A source either emits a complete snapshot or emits nothing and reports an error.

The Flight Control destination treats a successful snapshot as authoritative for the pipeline that produced it. It creates or updates the resources in the snapshot, leaves unchanged resources untouched, and deletes resources that the same pipeline created earlier but that the snapshot no longer contains. An empty snapshot is a valid desired state that asks for everything the pipeline manages to be removed.

## Choosing between the collector and ResourceSync

Flight Control offers two ways to populate a catalog from outside the API, and a single deployment can use both for different catalogs. Use ResourceSync when you author the catalog content yourself and want Git review and history. Use the collector when another system already owns the content and Flight Control only has to track it.

The two also differ in how strongly the imported content is protected. ResourceSync marks each resource with an owner that the API enforces, while the collector labels are a boundary the collector applies to itself. For the side-by-side comparison, see [Importing catalogs from external sources](../managing-catalogs.md#importing-catalogs-from-external-sources).

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

The layout is inspired by the [OpenTelemetry Collector configuration](https://opentelemetry.io/docs/collector/configuration/): components are declared under a section per kind and named with a `type[/name]` identifier, and pipelines wire those names together. The resemblance stops at the layout. The catalog collector is a separate program that shares no code with the OpenTelemetry Collector, its component set is specific to Flight Control catalogs, and no OpenTelemetry receiver, processor, exporter, or extension can be loaded into it.

### Which components are built, and how many

A source, a processor, or a destination is constructed only when a pipeline references it. A declaration that no pipeline uses is ignored, and its type does not even need a registered factory. Extensions are different: every declared extension is constructed and started whether or not a component references it, because an extension is a shared service capability rather than a pipeline stage.

Where a component is referenced more than once, the number of instances follows from how the pipeline graph is built:

| Kind | Instances |
|---|---|
| Extension | One per declared identifier. All of them are constructed and started. |
| Source | One per referenced identifier, however many pipelines reference it. The single instance hands each snapshot to every pipeline that uses it. |
| Processor | One per occurrence in a pipeline. Two pipelines that list the same processor identifier get a separate instance each. |
| Destination | One per referenced identifier, shared by every pipeline that targets it. A shared destination keeps the resources of each pipeline separate and prunes only its own. |

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

## Getting content into the collector

Every pipeline begins with a source. Start from the two sources the official collector ships, and fall back to writing your own only when neither fits.

### Built-in sources

| Source | How it obtains content | Use it for |
|---|---|---|
| `kubeflowmodelregistry` | Queries a Kubeflow Model Registry, as exposed by Red Hat OpenShift AI, on a poll interval and converts its registered models into catalog resources. | Mirroring a Model Registry without writing any code. |
| `http` | Serves an HTTP endpoint and waits. It never contacts an external system itself; it accepts complete snapshots that something else posts to it. | Sending content from your own adapter, and for testing a pipeline by hand. |

The `kubeflowmodelregistry` source is the only shipped source that talks to an external system. See [Importing a Kubeflow Model Registry](kubeflow-model-registry.md) for its workflow and its configuration.

The `http` source is what makes the collector usable with a system nothing ships a source for. You write a program that knows how to read your external system and how to express its content as Catalog and CatalogItem resources, and you post that as one JSON document. Everything after the post is handled for you: snapshot validation, processors, the Flight Control destination, labeling, and pruning. You do not have to implement any of the reconciliation rules, and you do not have to deploy or build a modified collector.

> [!WARNING]
> The `http` source performs no inbound authentication of any kind. Bind it to loopback or to a cluster-internal network, and never expose it through an Ingress, a Route, or a published routable port. Anyone who can reach it can replace or delete the catalog content the pipeline manages.

### Bringing your own integration

Two paths exist for a system that no shipped source covers.

**Post snapshots to the `http` source.** Your adapter runs as its own process on its own release cadence, in any language, and it needs no Flight Control credential of its own, because the collector holds the credential for the destination. This is the right choice for a bespoke or proprietary system, and when the conversion logic belongs to a team that does not work in the Flight Control repository.

**Compile a source into a collector distribution.** A source component runs inside the collector process and hands snapshots to the pipeline in memory, with no adapter to deploy and no HTTP hop. A source that polls can reuse the collector's polling loop, bounded backoff, startup preflight check, and component metrics, but the contract does not require polling: a source only has to run until it is cancelled and emit complete snapshots. This is the right choice for an API that several deployments share. It does couple the integration to a collector build, because the component is compiled in.

See the [custom catalog collector sources](../../../developer/catalog-collector-sources.md) developer guide for the component contracts and for how to build a distribution that includes your own source.

## Snapshot format

The HTTP source accepts a single JSON object with three required fields:

| Field | Type | Description |
|---|---|---|
| `revision` | string | An opaque, deterministic identifier of the desired content. It must be non-empty, and it must change only when the content changes. |
| `catalogs` | array | Desired Catalog resources. Send `[]` for an empty collection; the field may not be omitted. |
| `catalogItems` | array | Desired CatalogItem resources. Send `[]` for an empty collection; the field may not be omitted. |

Unknown fields are rejected, both at the top level and inside each resource. The maximum accepted body size is 16 MiB.

The request is answered in stages, and the status code says how far it reached:

| Response | How far the request reached |
|---|---|
| `405 Method Not Allowed` | The method was not `POST`. Nothing was read. |
| `415 Unsupported Media Type` | The `Content-Type` header was not `application/json`. The body was not read. |
| `400 Bad Request` | The body was read but is not a well-formed snapshot envelope: it is too large, it is not valid JSON, it carries an unknown field at any level, it has trailing data, or `revision`, `catalogs`, or `catalogItems` is missing or empty. |
| `502 Bad Gateway` | The envelope parsed, but the pipeline rejected the snapshot. |
| `204 No Content` | Every pipeline that uses this source accepted the snapshot. |

Two of those deserve care:

* A `502` does **not** mean the snapshot itself was valid. Snapshot validation, which checks the individual resources, duplicate names, and duplicate catalog item identities, runs inside the pipeline rather than in the HTTP handler. A resource that fails Flight Control API validation therefore produces `502`, exactly like a destination that could not be reached. The collector log names the stage that failed.
* A `204` does **not** prove that anything was written to Flight Control. It proves only that every pipeline using this source returned success. A pipeline whose destination is `debug` returns success after logging the snapshot and contacting no service at all. Confirm the result with the Flight Control CLI, not with the status code.

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

This procedure runs the collector as a local binary on your own machine. It wires an HTTP source to a Flight Control destination, posts a snapshot into the collector, and confirms that the catalog arrives in Flight Control. It then posts a second, changed snapshot and confirms that the update is applied.

A local binary is the shortest way to see the whole cycle, because every file the collector reads is a file you can edit directly. It is not the way to run the collector permanently. Once the pipeline behaves as you expect, deploy it with one of the supported installation forms:

| Where the collector should run | Procedure |
|---|---|
| Kubernetes or OpenShift, using the Helm chart | [Installing on Kubernetes or OpenShift](../../installing/installing-catalog-collector.md#installing-on-kubernetes-or-openshift) |
| A standalone Linux host, using the RPM and its Podman Quadlet unit | [Installing on a standalone Linux host](../../installing/installing-catalog-collector.md#installing-on-a-standalone-linux-host) |
| A container you run yourself | Run the collector image with the configuration file and every credential it names mounted into the container |

The configuration itself does not change between these forms. What changes is where the files live: in a container or a pod, every path in the configuration is a path inside the container, not a path on the host.

### Prerequisites

* A reachable Flight Control API endpoint, and an account that may read, create, update, and delete Catalog and CatalogItem resources.
* An authenticated Flight Control CLI, so that you can confirm the result. Run `flightctl login <server_url>` first; see [flightctl login](../../references/cli-commands.md#flightctl-login) for its flags and [Installing the Flight Control CLI](../../installing/installing-cli.md) to obtain the CLI.
* An API token that the Flight Control service accepts. How you obtain one depends on the authentication method your deployment uses; see [Authentication overview](../../installing/configuring-auth/overview.md).
* The CA bundle that signs the Flight Control API certificate, unless that certificate is already trusted by the system trust store.
* The collector binary. A package installation puts `flightctl-catalog-collector` on the `PATH`. A build from a clone of the Flight Control repository, with `make build-catalog-collector`, writes it to `bin/flightctl-catalog-collector` in the clone instead.
* The `curl` command.

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

4. Save the following configuration as `~/catalog-collector/collector.yaml`. Replace `<flightctl_api_hostname>` with the host name of your Flight Control API, and `<user_name>` with your own user name, so that every path is absolute:

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
        server: https://<flightctl_api_hostname>
        certificateAuthority: /home/<user_name>/catalog-collector/flightctl-ca.crt
        timeout: 30s
        auth:
          authenticator: bearertokenauth/flightctl

    pipelines:
      external-catalog:
        source: http/snapshots
        destination: flightctl/service
    ```

    The collector does not expand `~`, so the credential and the CA bundle must be given as absolute paths.

5. Start the collector. Use the command name when the binary came from a package:

    ```console
    flightctl-catalog-collector --config ~/catalog-collector/collector.yaml
    ```

    From a clone of the Flight Control repository, run the binary that `make build-catalog-collector` produced instead:

    ```console
    ./bin/flightctl-catalog-collector --config ~/catalog-collector/collector.yaml
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

    A `502` means the envelope parsed but the pipeline rejected the snapshot, either in snapshot validation or at the destination. Read the collector log, which names the failing stage.

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

    The `spec.shortDescription` field now carries the revised text. Removing the catalog item from the snapshot entirely and posting again asks for it to be deleted, because the snapshot is the complete desired state for the pipeline. The deletion succeeds only while no device or fleet references a version of that item; see [Deletion through omission](#deletion-through-omission).

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
* A snapshot delivered to this destination may not set either label itself. The destination rejects the snapshot if it does. This is a rule of the Flight Control destination, not of snapshots in general: the shared snapshot validation that runs before every destination does not look at labels, and a snapshot carrying those labels is accepted by the `debug` destination.

CatalogItems are pruned before their Catalogs.

### What the labels do not do

The labels are a boundary the collector applies to itself. They are not an access control.

* Any other client that is authorized to call the Flight Control API can still create, edit, and delete a resource carrying the collector labels. The API, the CLI, and the UI treat it like any other Catalog or CatalogItem.
* A manual edit to a resource the collector manages is not permanent, as long as both labels are still present. The collector compares each desired resource against the live one on every synchronization and rewrites it when the specification or the labels differ, so the next successful snapshot restores the collector's view. That repair is a normal write, so it is refused like any other write that would change a catalog item version a device or fleet is using.
* Removing the labels by hand does not transfer the resource. It takes the resource outside the pipeline boundary, so the collector stops pruning it and the next snapshot that wants the same name fails instead of overwriting it.

ResourceSync uses a different mechanism. It sets `metadata.owner` on the resources it creates, and the Flight Control API refuses a request that changes the specification of an owned resource or deletes it. Ownership protects the content rather than the whole resource: a request that updates only the labels is still accepted. If you need a catalog whose content cannot be changed outside its import path, use ResourceSync. See [Importing catalogs using ResourceSync](../managing-catalogs.md#importing-catalogs-using-resourcesync).

### Deletion through omission

The collector has no delete instruction. A resource is removed by leaving it out of the next snapshot: the destination lists the resources carrying its own labels for this pipeline, compares them against the snapshot, and deletes what the snapshot no longer asks for.

Two things follow from that.

**Omission is a request, not a guarantee.** The destination issues a delete, and the Flight Control API may refuse it. The API rejects the deletion of a catalog item while any device or fleet still references one of its versions, and answers with a conflict naming those versions. The collector reports the failure and the resource stays. Posting an empty snapshot therefore does not guarantee that the pipeline's content disappears; it guarantees only that the collector asks for all of it to be deleted. Retire the references first, then let the next cycle prune.

**A sync failure is not a rollback.** Reconciliation is not transactional. Writes are applied one resource at a time, and a failure part-way through leaves the earlier writes committed, so the catalog can sit between the old and the new desired state until a later cycle succeeds. Pruning is ordered to be safer, but it is not atomic either:

* No deletion is attempted until every desired write has succeeded and both complete lists of managed resources have been retrieved. A failure in collection, in a write, or in either list therefore deletes nothing at all.
* Once deletion starts, the deletes run one after another. If one of them fails, the deletes already issued stay applied and the rest are abandoned.

Because a source always re-sends the complete desired state, a later successful cycle converges on it. The exception is a resource the API refuses to delete: that one keeps failing until the references to it are removed.

### Renaming a pipeline

The pipeline name is part of a label value, so renaming a pipeline changes which resources the pipeline recognizes as its own.

Renaming does not delete anything. The resources created under the old name keep the old label value, and no pipeline claims them any more: nothing prunes them, and they remain in the catalog exactly as they were. The renamed pipeline is also unable to take them over, because the collector never adopts a resource outside its boundary. It fails on the first desired name that already exists under the old label value, and it keeps failing until that is resolved.

Keep the pipeline name stable. If you must rename one, remove the resources carrying the old label value before you start the renamed pipeline. List them first:

```console
flightctl get catalogs -l flightctl.io/catalog-collector-pipeline=<old_pipeline_name>
```

```console
flightctl get catalogitems -l flightctl.io/catalog-collector-pipeline=<old_pipeline_name>
```

Then delete each one by name, catalog items before their catalogs. A catalog item that a device or fleet still references cannot be deleted; see [Deletion through omission](#deletion-through-omission).

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

The `healthcheck` extension serves a liveness path and a readiness path. Both accept `GET` and `HEAD` and answer with a small JSON body.

| Path | Response |
|---|---|
| `livePath` | `200 OK` with `{"status":"live"}` whenever the health server is running. It does not depend on the pipelines. |
| `readyPath` | `503 Service Unavailable` with `{"status":"not_ready"}` until the collector finishes startup, then `200 OK` with `{"status":"ready"}`. Shutdown returns it to `503`. |

Readiness therefore reports that startup finished, in the sense described under [Startup sequence](#startup-sequence). It is not a statement that the pipeline is working.

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
| `204 No Content` | Every pipeline using this source reported success. With a `flightctl` destination that means the content was reconciled; with a `debug` destination it means only that the snapshot was logged. |
| `400 Bad Request` | The body is not a well-formed snapshot envelope. The response text names the problem. |
| `405 Method Not Allowed` | The request method was not `POST`. |
| `415 Unsupported Media Type` | The request did not set `Content-Type: application/json`. |
| `502 Bad Gateway` | The envelope parsed, but snapshot validation, a processor, or a destination rejected it. The collector log names the failing stage. |

A `404` comes from the HTTP server itself and means the request went to a path other than the configured `path`.

### Common failures

| Symptom | Likely cause |
|---|---|
| Startup fails naming a configuration path | A misspelled field, an unknown field, or an unset environment variable. Decoding is strict at every level. |
| Startup fails naming an extension | A component references an authenticator identifier that no extension declares, or the extension does not provide the capability the component needs. |
| Reconciliation fails with a name collision | A resource with that name already exists outside the pipeline boundary. See [What the labels do not do](#what-the-labels-do-not-do). |
| The collector is ready, but nothing reaches Flight Control | Readiness does not exercise the destination credential. Check the pipeline sync metrics and the log. |
| Reconciliation fails on every cycle after a pipeline rename | The renamed pipeline writes a new label value, so the resources created under the old name are left behind and the new name collides with them. See [Renaming a pipeline](#renaming-a-pipeline). |
| A catalog item is not removed although the snapshot omits it | The API refused the deletion because a device or fleet still references one of its versions. The log carries the conflict and the affected versions. See [Deletion through omission](#deletion-through-omission). |
| The catalog is part-way between two states after a failure | Reconciliation is not transactional. Writes already applied stay applied. A later successful cycle converges on the desired state. |

## Further reading

* [Installing the catalog collector](../../installing/installing-catalog-collector.md)
* [Importing a Kubeflow Model Registry](kubeflow-model-registry.md)
* [Catalog collector configuration reference](../../references/catalog-collector.md)
* [Software catalog](../managing-catalogs.md)
* [Writing custom catalog collector sources](../../../developer/catalog-collector-sources.md)
