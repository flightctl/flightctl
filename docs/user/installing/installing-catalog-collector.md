# Installing the catalog collector

The Flight Control catalog collector is an optional add-on that imports software catalogs from external systems into Flight Control. You can deploy and upgrade it independently of the Flight Control service. It can run in the same cluster, in another cluster, or on a Linux host, provided it can reach the Flight Control API and the external services required by its sources. See [Catalog collector overview](../using/catalog-collector/overview.md) to decide when to use it.

Choose the installation method for your environment:

* **Kubernetes or OpenShift:** Install the Helm chart.
* **Standalone Linux host:** Install the RPM sub-package, which provides a Podman Quadlet unit.

The Helm chart and RPM sub-package follow Flight Control’s release versioning. Both run the collector container image and use the same configuration format.

## Before you begin

* Choose the sources and destinations for your pipelines. The collector ships no default pipeline and requires a configuration to start.
* Gather any credentials required by those sources and destinations. For the Flight Control destination, ensure the credentials permit reading, writing, and deleting Catalogs and CatalogItems in the target organization.
* Gather CA bundles for endpoints whose TLS certificates are not trusted by the system trust store.
* Prepare the collector configuration using the [Catalog collector configuration reference](../references/catalog-collector.md).

> [!IMPORTANT]
> A collector instance can run multiple pipelines, with different or shared sources and destinations.
>
> Pipelines targeting the same Flight Control organization must use distinct pipeline IDs and manage separate catalogs, whether they run in one collector instance or several.
>
> Run only one active collector instance for each pipeline ID in that organization. Instances using the same ID can update or prune each other's resources.

## Installing on Kubernetes or OpenShift

Install the collector using the `flightctl-catalog-collector` Helm chart.

### Prerequisites

* Access to a Kubernetes cluster, with permission to install the collector in the target namespace.
* The `helm` and `kubectl` commands installed.
* A reachable Flight Control API endpoint.

### Procedure

1. Download and extract the collector Helm chart. Replace `<version>` with the chart version you want to install:

    ```console
    helm pull oci://quay.io/flightctl/charts/flightctl-catalog-collector \
      --version <version> --untar
    ```

    If you use a Flight Control repository checkout, skip this download and run the remaining commands from the repository root. In both the chart and values file paths below, replace `./flightctl-catalog-collector` with `./deploy/helm/flightctl-catalog-collector`.

2. Install the chart using the vanilla example to verify the collector deployment. This example runs without external services and uses a debug destination to log received snapshots.

    Replace `<namespace>` with an existing target namespace:

    ```console
    helm install flightctl-catalog-collector ./flightctl-catalog-collector \
      --namespace <namespace> \
      --values ./flightctl-catalog-collector/examples/values-vanilla.yaml
    ```

3. Wait for the deployment to become ready:

    ```console
    kubectl -n <namespace> rollout status deployment/flightctl-catalog-collector
    ```

4. If the deployment does not become ready, inspect the collector logs:

    ```console
    kubectl -n <namespace> logs deployment/flightctl-catalog-collector
    ```

The verification commands use the release name `flightctl-catalog-collector` and the chart's default resource names. Adjust the Deployment name if you change either.

After verifying the deployment, configure your pipelines as described in [Supplying the collector configuration](#supplying-the-collector-configuration). Additional configurations are available in the chart's `examples/` directory.

### Supplying the collector configuration

Prepare your collector configuration using the [Catalog collector configuration reference](../references/catalog-collector.md). The chart's `examples/` directory provides sample Helm values files you can adapt.

Supply the configuration through exactly one of these Helm values:

* `config.content`: Include the collector configuration in your Helm values file. Applying a configuration change with `helm upgrade` restarts the collector automatically.
* `config.existingName`: Reference a ConfigMap in the collector's namespace containing the collector configuration. Restart the Deployment after changing that ConfigMap.

For Flight Control destination settings and required permissions, see [Flight Control destination](../references/catalog-collector.md#flightctl). To configure OAuth 2.0 client credentials, see [OAuth2 client authentication](../references/catalog-collector.md#oauth2client).

When health or metrics endpoints are enabled, configure them to listen on `0.0.0.0` inside the container. Their ports and the health probe paths must match the chart settings. If your configuration omits the `healthcheck` extension, set `health.enabled` to `false`.

For detailed Helm settings, including configuration mounting, credential and CA bundle mounts, file permissions, health probes, metrics discovery, and replica count, see `README.md` in the chart directory. This file is included in the downloaded chart.

Apply your Helm values file:

```console
helm upgrade flightctl-catalog-collector ./flightctl-catalog-collector \
  --namespace <namespace> \
  --values <values_file>
```

If you use a Flight Control repository checkout, replace `./flightctl-catalog-collector` with `./deploy/helm/flightctl-catalog-collector`.

If you use `config.existingName` and change the referenced ConfigMap, restart the collector:

```console
kubectl -n <namespace> rollout restart deployment/flightctl-catalog-collector
```

## Installing on a standalone Linux host

On RHEL and compatible distributions, install the `flightctl-catalog-collector` RPM package. It provides a Podman Quadlet unit that runs the collector as a systemd service.

### Prerequisites

* A host with Podman and systemd, and the Flight Control RPM repository configured.
* Permission to run commands with `sudo`.
* The `curl` command installed.

### Procedure

1. Install the package:

    ```console
    sudo dnf install flightctl-catalog-collector
    ```

2. Copy the vanilla example configuration into place to verify the installation. This example uses an HTTP snapshot source and a debug destination and has no external dependencies:

    ```console
    sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/config-vanilla.yaml \
      /etc/flightctl/flightctl-catalog-collector/config.yaml
    ```

3. Reload systemd and start the collector:

    ```console
    sudo systemctl daemon-reload
    sudo systemctl start flightctl-catalog-collector
    ```

    The collector also starts at boot when its configuration file exists.

4. Check the readiness endpoint:

    ```console
    curl -fsS http://127.0.0.1:13133/readyz
    ```

    When the collector is ready, the response is:

    ```text
    {"status":"ready"}
    ```

    If the collector is still starting, repeat the check.

5. If the collector does not become ready, inspect its status and logs:

    ```console
    sudo systemctl status flightctl-catalog-collector
    sudo journalctl -u flightctl-catalog-collector -n 50
    ```

### Supplying the standalone configuration

After verifying the installation, replace `/etc/flightctl/flightctl-catalog-collector/config.yaml` with your pipeline configuration. See the [Catalog collector configuration reference](../references/catalog-collector.md).

Example configurations are installed in `/usr/share/flightctl/flightctl-catalog-collector/examples/`.

For Flight Control destination settings and required permissions, see [Flight Control destination](../references/catalog-collector.md#flightctl). To configure OAuth 2.0 client credentials, see [OAuth2 client authentication](../references/catalog-collector.md#oauth2client).

The unit mounts the configuration directory read-only inside the container:

| Host directory | Container directory |
|---|---|
| `/etc/flightctl/flightctl-catalog-collector/` | `/etc/flightctl/catalog-collector/` |

Place credential files and CA bundles under the host directory and reference their container paths in the configuration. Files stored under this directory need no additional volume mounts.

For example, a host file at `/etc/flightctl/flightctl-catalog-collector/credentials/token` is available inside the container at `/etc/flightctl/catalog-collector/credentials/token`.

### Preparing credential files and CA bundles

The collector runs with UID 1001 and GID 0. Credential files must be readable by GID 0. Use ownership `root:root` and mode `0640`:

```console
sudo install -d -m 0750 -o root -g root \
  /etc/flightctl/flightctl-catalog-collector/credentials

sudo install -m 0640 -o root -g root <credential_file> \
  /etc/flightctl/flightctl-catalog-collector/credentials/<filename>
```

Replace `<credential_file>` with the source file and `<filename>` with the name you reference in the collector configuration.

CA bundles can use mode `0644`:

```console
sudo install -d -m 0755 -o root -g root \
  /etc/flightctl/flightctl-catalog-collector/certs

sudo install -m 0644 -o root -g root <ca_bundle> \
  /etc/flightctl/flightctl-catalog-collector/certs/<filename>
```

### Configuring published ports

The unit publishes the health endpoint at `127.0.0.1:13133` and the metrics endpoint at `127.0.0.1:8888` on the host. Configure these endpoints to listen on `0.0.0.0:13133` and `0.0.0.0:8888`, respectively, inside the container.

Keep these host ports bound to loopback because the endpoints do not authenticate requests.

Ports that configured sources listen on are not published by default. To publish the vanilla example's HTTP snapshot listener at `127.0.0.1:8080`, install the supplied Quadlet drop-in:

```console
sudo mkdir -p /etc/containers/systemd/flightctl-catalog-collector.container.d

sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/vanilla-publish-8080.conf \
  /etc/containers/systemd/flightctl-catalog-collector.container.d/

sudo systemctl daemon-reload
sudo systemctl restart flightctl-catalog-collector
```

Use drop-ins for changes to the Quadlet unit. Package upgrades preserve administrator-supplied drop-ins.

### Applying changes and managing the service

Restart the collector after changing its configuration:

```console
sudo systemctl restart flightctl-catalog-collector
```

If repeated startup failures leave the service in the failed state, fix the cause, clear the failure state, and start it again:

```console
sudo systemctl reset-failed flightctl-catalog-collector
sudo systemctl start flightctl-catalog-collector
```

Upgrade the collector:

```console
sudo dnf upgrade flightctl-catalog-collector
```

The upgrade restarts the service with the new collector image. Administrator-supplied configuration files and drop-ins are preserved. On an air-gapped host, ensure the new image is available to Podman before upgrading.

Remove the collector:

```console
sudo dnf remove flightctl-catalog-collector
```

Removal stops the service. Administrator-supplied configuration, credentials, CA bundles, and drop-ins remain in place.

## Verifying the installation

Readiness confirms that startup completed and any supported preflight checks passed. It does not confirm that a pipeline has successfully synchronized a snapshot.

The Flight Control destination's startup preflight checks connectivity, authentication, and permission to list Catalogs and CatalogItems. It does not verify create, update, or delete permissions. After startup, verify that a synchronization cycle succeeds and that the expected resources are present.

The following checks assume health and metrics are enabled on their default ports. On Kubernetes or OpenShift, first forward these ports and leave the command running in a separate terminal:

```console
kubectl -n <namespace> port-forward deployment/flightctl-catalog-collector \
  13133:13133 8888:8888
```

1. Check the readiness endpoint:

    ```console
    curl -fsS http://127.0.0.1:13133/readyz
    ```

    When the collector is ready, the endpoint returns `200 OK` with:

    ```text
    {"status":"ready"}
    ```

2. Allow a snapshot to pass through your pipeline. For a polling source, allow a collection cycle to complete. For an HTTP source, submit a snapshot.

    Read the collector metrics:

    ```console
    curl -fsS http://127.0.0.1:8888/metrics | grep flightctl_catalogcollector_
    ```

    Confirm that the pipeline synchronization counter reports a successful synchronization for your pipeline, with `outcome="success"` and a value greater than zero. See [Metrics](../references/catalog-collector.md#metrics) for metric descriptions.

    If collection or synchronization fails, inspect the collector logs using the commands in your installation procedure.

3. For a pipeline using the Flight Control destination, confirm that the expected catalogs and items are present. Use the Flight Control CLI configured for the target service and organization:

    ```console
    flightctl get catalogs
    flightctl get catalogitems --catalog <catalog_name>
    ```

    For a pipeline using the debug destination, verify that received snapshots appear in the collector logs.

## Further reading

* [Catalog collector overview](../using/catalog-collector/overview.md)
* [Catalog collector configuration reference](../references/catalog-collector.md)
* [Importing a Kubeflow Model Registry](../using/catalog-collector/kubeflow-model-registry.md)
