# Installing the catalog collector

The Flight Control catalog collector is an optional add-on that imports software catalogs from an external system into Flight Control. It is packaged and released separately from the Flight Control service, and it can run next to the service, in another namespace, or on another host. Install it only when you need to mirror an external catalog; see [Catalog collector overview](../using/catalog-collector/overview.md) for when that applies.

Two deployment forms are supported:

* A Helm chart for Kubernetes and OpenShift.
* An RPM sub-package that installs a Podman Quadlet unit for standalone Linux hosts.

Both run the same collector image and read the same configuration file. Choose the one that matches where the collector should run; it does not have to be where the Flight Control service runs.

## Before you begin

* Decide which source and destination the collector uses. The collector ships no default pipeline, and it refuses to start without a configuration.
* Collect the credentials the pipeline needs: a credential for the upstream system, and a credential for the Flight Control API.
* Collect the CA bundles for any endpoint that is not signed by a certificate in the system trust store.
* Read [Catalog collector configuration reference](../references/catalog-collector.md) and prepare the configuration file.

> [!IMPORTANT]
> Run one collector instance per pipeline target. The collector owns the catalogs it writes, so two instances polling the same upstream registry and reconciling the same catalog race each other into conflicting updates.

## Installing on Kubernetes or OpenShift

The collector has its own chart, `flightctl-catalog-collector`. It is deliberately not a sub-chart of `flightctl`, so it can be installed in a different namespace or against a remote Flight Control service and upgraded on its own cadence.

### Prerequisites

* Access to a Kubernetes or OpenShift cluster and permission to create a Deployment, a ConfigMap, a Service, and a ServiceAccount in the target namespace.
* The `helm` command.
* A reachable Flight Control API endpoint.

### Procedure

1. Obtain the chart. The `examples/` directory is part of the packaged chart, so no clone of the Flight Control repository is required. Pull the packaged chart and unpack it:

    ```console
    helm pull <chart_reference> --version <version> --untar
    ```

    Replace `<chart_reference>` with the chart location your distribution publishes. From a clone of the Flight Control repository, skip this step and use `deploy/helm/flightctl-catalog-collector` as the chart path in the next step.

2. Install the chart with one of the shipped example value files. A bare `helm install` with no values fails on purpose, because the chart has no default pipeline:

    ```console
    helm install catalog-collector ./flightctl-catalog-collector \
      --values ./flightctl-catalog-collector/examples/values-vanilla.yaml
    ```

3. Confirm that the deployment rolls out:

    ```console
    kubectl -n <namespace> rollout status deployment/catalog-collector-flightctl-catalog-collector
    ```

4. Read the log if the pod does not become ready:

    ```console
    kubectl -n <namespace> logs deployment/catalog-collector-flightctl-catalog-collector
    ```

The two shipped examples are:

| File | Pipeline |
|---|---|
| `examples/values-vanilla.yaml` | HTTP snapshot source to debug destination. No external dependencies. Use it to verify that the image, the probes, and the metrics endpoint work in your cluster. |
| `examples/values-rhoai-to-flightctl.yaml` | Kubeflow Model Registry to Flight Control in the same cluster, with the pod ServiceAccount token for the registry and OAuth2 client credentials for Flight Control. |

### Supplying the collector configuration

Exactly one of the following chart values must be set. Setting both, or neither, fails the render with an explicit message.

| Value | Meaning |
|---|---|
| `config.content` | Inline collector configuration. The chart renders it into a ConfigMap it owns and adds a `checksum/config` pod annotation, so `helm upgrade` restarts the collector when the configuration changes. |
| `config.existingName` | Name of a ConfigMap managed outside the chart, for example by a GitOps controller. No checksum annotation is added, so roll the deployment yourself after changing it. |
| `config.key` | Key inside the ConfigMap that holds the configuration. The default is `config.yaml`. |
| `config.mountPath` | Directory the ConfigMap is mounted into. The container runs with `--config <mountPath>/<key>`. The default is `/etc/flightctl/catalog-collector`. |

### Matching the chart values to the configuration

The chart wires the probes and the metrics port, but the endpoints themselves come from the collector configuration. The two must agree:

| Chart value | Collector configuration field | Value to use |
|---|---|---|
| `health.port`, `health.livePath`, `health.readyPath` | `extensions.healthcheck.endpoint`, `.livePath`, `.readyPath` | `0.0.0.0:13133`, `/livez`, `/readyz` |
| `metrics.port` | `service.metrics.endpoint` | `0.0.0.0:8888` |

Both endpoints must bind `0.0.0.0`. The collector defaults to `localhost`, which the kubelet and Prometheus cannot reach, so the pod would never become ready and would never be scraped. The metrics path is fixed at `/metrics` and is not a chart value.

Set `health.enabled=false` only when the configuration omits the `healthcheck` extension. Otherwise the probes target a closed port.

### Mounting credentials and CA bundles

Mount credentials and CA bundles with `extraVolumes` and `extraVolumeMounts`, then reference the **path inside the container** from the collector configuration fields `tokenFile`, `clientIdFile`, `clientSecretFile`, and `certificateAuthority`. A path that exists on your workstation or on a node means nothing to the collector.

The container runs as uid 1001 and gid 0. A regular Secret volume stays owned by `root:root` whatever uid the pod runs as, so grant the read through the group bit:

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

`defaultMode: 0400` grants the bits to an owner the collector is not, and the read fails with a permission error. Do not add `fsGroup`; it is unnecessary once the group bit is set.

Values may also be injected through `env` and `envFrom` and referenced in the configuration as `${env:NAME}`.

### Authenticating to Flight Control

Prefer OAuth2 client credentials over a static bearer token. Register a confidential client in the identity provider that Flight Control authenticates against and mount its identifier and secret as separate keys of one Secret.

Grant the client every permission reconciliation uses on both Catalog and CatalogItem resources:

| Permission | Why reconciliation needs it |
|---|---|
| Read and list | Each desired resource is read before it is written, and the complete set of resources carrying the pipeline labels is listed on every cycle. |
| Create and update | Desired resources are written. |
| Delete | Resources the pipeline created earlier but that the snapshot no longer contains are pruned. |

Delete is not optional. Pruning is part of every cycle, so a credential that may only create and update fails as soon as anything has to be removed.

Configure the extension and point the destination at it:

```yaml
extensions:
  oauth2client/flightctl:
    clientIdFile: /etc/flightctl/catalog-collector/oauth/client-id
    clientSecretFile: /etc/flightctl/catalog-collector/oauth/client-secret
    tokenUrl: https://<oidc_token_endpoint>
    scopes:
      - openid
    certificateAuthority: /etc/flightctl/catalog-collector/certs/oidc-ca.crt
    expiryBuffer: 30s

destinations:
  flightctl/service:
    server: https://<flightctl_api_hostname>
    auth:
      authenticator: oauth2client/flightctl
```

Take `<oidc_token_endpoint>` from your own authorization server, usually from the `token_endpoint` field of its OpenID Connect discovery document. A Keycloak realm, for example, publishes it as `https://<host>/realms/<realm>/protocol/openid-connect/token`, but the path differs between providers.

The collector exchanges the client credentials for a short-lived access token, so no long-lived token is stored. `clientId` and `clientSecret` may be given inline instead, but the values then appear in the ConfigMap and in `helm get values`.

### Replica count and scaling

`replicaCount` accepts only `0` or `1`, and any other value fails the render. Two instances would reconcile the same catalog concurrently. Use `replicaCount=0` to pause the collector without uninstalling the release. The deployment strategy is `Recreate` for the same reason.

### Exposing metrics

| Value | Effect |
|---|---|
| `metrics.podAnnotations` | Adds the `prometheus.io/*` annotations used by annotation-based discovery. Enabled by default. |
| `metrics.serviceMonitor.enabled` | Creates a ServiceMonitor. Requires the Prometheus Operator CRDs and `service.enabled`. |

> [!WARNING]
> Neither the metrics endpoint nor the health endpoint authenticates its callers. Keep the Service of type `ClusterIP`, which is the default, and do not place an Ingress, a Route, or a load balancer in front of it without an authenticating proxy.

A Service is created only when it would carry at least one port. With metrics and health both disabled and no `service.extraPorts`, the chart skips it; use `kubectl port-forward` in that case.

For the complete list of chart values, including pod labels, extra ports, security context, and resource requests, see the [chart README](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl-catalog-collector/README.md).

## Installing on a standalone Linux host

On RHEL and compatible distributions the collector ships as the `flightctl-catalog-collector` RPM sub-package. It installs a Podman Quadlet unit that systemd manages. The unit is not part of `flightctl.target`, so the collector can be installed on a host that does not run the Flight Control service.

### Prerequisites

* A host with `podman` and systemd, and access to the Flight Control RPM repository.
* Permission to run commands with `sudo`.

### Procedure

1. Install the sub-package:

    ```console
    sudo dnf install flightctl-catalog-collector
    ```

2. Copy an example configuration into place and edit it:

    ```console
    sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/config-vanilla.yaml \
        /etc/flightctl/flightctl-catalog-collector/config.yaml
    ```

3. Reload the systemd generators and start the service:

    ```console
    sudo systemctl daemon-reload
    sudo systemctl start flightctl-catalog-collector
    ```

    The `daemon-reload` step is required, not advisory. Quadlet is a systemd generator, so `flightctl-catalog-collector.service` does not exist until the generators run again.

4. Confirm that the collector finished starting:

    ```console
    curl -s http://127.0.0.1:13133/readyz
    ```

    The expected output is:

    ```text
    {"status":"ready"}
    ```

    Before startup completes, and again during shutdown, the endpoint answers `503 Service Unavailable` with `{"status":"not_ready"}`.

### Installed paths

| Path | Contents |
|---|---|
| `/usr/share/containers/systemd/flightctl-catalog-collector.container` | The Quadlet unit |
| `/etc/flightctl/flightctl-catalog-collector/` | Administrator-supplied configuration, credential files, and CA bundles |
| `/usr/share/flightctl/flightctl-catalog-collector/examples/` | Example configurations and a port-publishing drop-in |

The host directory `/etc/flightctl/flightctl-catalog-collector/` is mounted read-only at `/etc/flightctl/catalog-collector/` in the container. Every path written inside `config.yaml` is a **container** path. Writing a host path there is the most common mistake, and it fails at startup with an error naming a path that plainly exists on the host.

### Credential file permissions

The image runs as uid 1001 and gid 0, and the unit runs under rootful Podman, so those identifiers are the identifiers on the host. Mode `0600 root:root` is therefore not readable by the collector. Grant gid 0 instead:

```console
sudo install -d -m 0750 -o root -g root /etc/flightctl/flightctl-catalog-collector/credentials
```

```console
sudo install -m 0640 -o root -g root /dev/null \
    /etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token
```

CA bundles are not secret, so mode `0644` is sufficient for those.

### Published ports

The unit publishes the health endpoint on `127.0.0.1:13133` and the metrics endpoint on `127.0.0.1:8888`, because neither endpoint is authenticated. Inside the container both must bind `0.0.0.0`, or the published ports reach nothing.

Source ports, such as the HTTP snapshot listener, are not published. Add them with a Quadlet drop-in rather than editing the unit, because a package upgrade replaces the unit but leaves drop-ins alone:

```console
sudo mkdir -p /etc/containers/systemd/flightctl-catalog-collector.container.d
```

```console
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/vanilla-publish-8080.conf \
    /etc/containers/systemd/flightctl-catalog-collector.container.d/
```

### Lifecycle

| Task | Command or behavior |
|---|---|
| Apply a configuration change | `sudo systemctl restart flightctl-catalog-collector`. The collector reads its configuration only at startup and has no reload signal. |
| Rotate a credential file | No restart needed. A file referenced by `tokenFile` is read before every request. Files referenced by `clientIdFile` and `clientSecretFile` are read only when a new access token is acquired, so a cached token stays in use until it is replaced. Restart the collector when a rotation must take effect at once. |
| Inspect failures | `systemctl status flightctl-catalog-collector` and `journalctl -u flightctl-catalog-collector -n 50`. |
| Recover from the restart limit | After five failures within five minutes the unit enters the failed state. Fix the configuration, then run `sudo systemctl reset-failed flightctl-catalog-collector` and start it again. |
| Behavior at boot | The unit carries `ConditionPathExists` on the configuration file, so it is skipped on a host with no configuration. That is the expected state after a fresh install. |
| Upgrade | `sudo dnf upgrade flightctl-catalog-collector` replaces the unit with one pinned to the new image tag and restarts the service. Configuration and drop-ins are untouched. |
| Remove | `sudo dnf remove flightctl-catalog-collector` stops and disables the service. The configuration directory is left in place because it holds credentials. |

On an air-gapped host, mirror the collector image before upgrading, or the restart fails with a pull error.

For the complete unit contract, including restart bounds and verification commands, see the [standalone README](https://github.com/flightctl/flightctl/blob/main/deploy/podman/flightctl-catalog-collector/README.md).

## Verifying the installation

1. Confirm that startup finished. The endpoint answers `200 OK` with `{"status":"ready"}` once the collector is ready, and `503 Service Unavailable` with `{"status":"not_ready"}` before that:

    ```console
    curl -s http://127.0.0.1:13133/readyz
    ```

    Readiness reports that the process finished starting. It is not a report that the pipeline works. Preflight is an optional capability that a source may implement. A source that implements it, such as `kubeflowmodelregistry`, is validated during startup, before the collector reports ready. A source that does not, such as `http`, contributes nothing to readiness. No preflight runs against a destination, so readiness never proves that the Flight Control credential is accepted.

2. Allow at least one poll interval to pass, then read the metrics:

    ```console
    curl -s http://127.0.0.1:8888/metrics | grep flightctl_catalogcollector_
    ```

3. Confirm that the catalog content arrived:

    ```console
    flightctl get catalogs
    ```

Readiness says nothing about the work that follows it. Collecting a snapshot and reconciling it happen after startup, on the poll interval, and either can fail on its own. The log names the failing stage.

## Further reading

* [Catalog collector overview](../using/catalog-collector/overview.md)
* [Catalog collector configuration reference](../references/catalog-collector.md)
* [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md)
