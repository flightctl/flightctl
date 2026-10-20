# flightctl-catalog-collector (standalone Linux)

Quadlet unit and example configurations for running the Flight Control catalog collector as a systemd-managed Podman container. These files are provided by the `flightctl-catalog-collector` RPM package.

This README describes the unit, host paths, and service lifecycle. For installation procedures and collector configuration, see:

* [Installing the catalog collector](https://github.com/flightctl/flightctl/blob/main/docs/user/installing/installing-catalog-collector.md)
* [Catalog collector overview](https://github.com/flightctl/flightctl/blob/main/docs/user/using/catalog-collector/overview.md)
* [Catalog collector configuration reference](https://github.com/flightctl/flightctl/blob/main/docs/user/references/catalog-collector.md)

## Installed layout

| Path | Purpose |
|---|---|
| `/usr/share/containers/systemd/flightctl-catalog-collector.container` | Package-managed Quadlet unit |
| `/etc/flightctl/flightctl-catalog-collector/` | Administrator configuration, credential files, and CA bundles |
| `/usr/share/flightctl/flightctl-catalog-collector/examples/` | Example configurations and the HTTP port-publishing drop-in |
| `/etc/containers/systemd/flightctl-catalog-collector.container.d/` | Administrator-supplied Quadlet drop-ins |

Use drop-ins to customize the unit. Package upgrades replace the vendor unit.

## Host and container paths

The unit mounts the configuration directory read-only:

| Host directory | Container directory |
|---|---|
| `/etc/flightctl/flightctl-catalog-collector/` | `/etc/flightctl/catalog-collector/` |

Paths inside the collector configuration must use the container directory. This applies to `tokenFile`, `clientIdFile`, `clientSecretFile`, and `certificateAuthority`.

For example:

| Host file | Configuration value |
|---|---|
| `/etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token` | `/etc/flightctl/catalog-collector/credentials/model-registry-token` |

The unit starts the container with:

```text
--config /etc/flightctl/catalog-collector/config.yaml
```

## Getting started

Install the package after configuring the Flight Control RPM repository:

```shell
sudo dnf install flightctl-catalog-collector
```

The unit has the following startup condition:

```ini
ConditionPathExists=/etc/flightctl/flightctl-catalog-collector/config.yaml
```

If the file is absent, systemd skips starting the service. The condition checks only that the file exists; the collector validates its contents during startup.

On a fresh installation, copy the vanilla example:

```shell
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/config-vanilla.yaml \
  /etc/flightctl/flightctl-catalog-collector/config.yaml
```

Adapt the configuration if needed:

```shell
sudoedit /etc/flightctl/flightctl-catalog-collector/config.yaml
```

Start the service:

```shell
sudo systemctl start flightctl-catalog-collector
```

The RPM runs `systemctl daemon-reload` during installation. Run it again after adding or changing a Quadlet file or drop-in. Changing only the collector YAML requires restarting the service, not regenerating the unit.

`ConditionPathExists` is evaluated when the service is requested to start. Creating the configuration file does not automatically start a previously skipped service.

## Credential file permissions

The official image uses UID `1001` and GID `0`. With the supplied rootful Podman setup and default user namespace mapping, credential files must be readable by that identity.

A file owned by `root:root` with mode `0600` is not readable by UID `1001`. Use group-readable files:

```shell
sudo install -d -m 0750 -o root -g root \
  /etc/flightctl/flightctl-catalog-collector/credentials
```

Install a token file:

```shell
sudo install -m 0640 -o root -g root <credential_file> \
  /etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token
```

Replace `<credential_file>` with a file containing the registry token.

Use the same ownership and permissions for OAuth client credential files. CA bundles can use mode `0644` because their certificate contents are not secret.

Check host ownership and permissions without printing the credential:

```shell
sudo stat -c '%U:%G %a %n' \
  /etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token
```

If you customize the container user, groups, or user namespace mapping, adjust file permissions accordingly.

For Kubernetes Secret mounts, see [File permissions in the chart README](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl-catalog-collector/README.md#file-permissions).

### Environment variables

The collector supports environment references such as `${env:NAME}` in component configurations.

To populate the container environment from a host file, use Quadlet's `[Container] EnvironmentFile` setting. A systemd `[Service] EnvironmentFile` alone does not automatically forward those variables into the container.

For example, create this drop-in:

```text
/etc/containers/systemd/flightctl-catalog-collector.container.d/10-environment.conf
```

With the following contents:

```ini
[Container]
EnvironmentFile=/etc/flightctl/flightctl-catalog-collector/collector.env
```

The environment file contains one assignment per line:

```text
REGISTRY_TOKEN=<token>
```

Keep a credential-bearing environment file owned by root with mode `0600`. Podman reads this host file before creating the container.

After adding the drop-in:

```shell
sudo systemctl daemon-reload
```

```shell
sudo systemctl restart flightctl-catalog-collector
```

Changes to the environment file also require a restart. Environment references are resolved when the collector starts.

Prefer mounted credential files when you need the authentication extensions' file-rotation behavior.

## Examples

The RPM installs these files under `/usr/share/flightctl/flightctl-catalog-collector/examples/`:

| File | Purpose |
|---|---|
| `config-vanilla.yaml` | HTTP snapshot source to debug destination, with health and metrics enabled. No external dependencies. |
| `config-rhoai-to-flightctl.yaml` | Kubeflow Model Registry to Flight Control, with bearer authentication for the registry, OAuth client credentials for Flight Control, CA bundles, and selection filters. |
| `vanilla-publish-8080.conf` | Quadlet drop-in publishing the HTTP snapshot listener on host loopback port `8080`. |

### Running the vanilla example end to end

With the vanilla configuration installed, publish its HTTP listener using the supplied drop-in:

```shell
sudo mkdir -p \
  /etc/containers/systemd/flightctl-catalog-collector.container.d
```

```shell
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/vanilla-publish-8080.conf \
  /etc/containers/systemd/flightctl-catalog-collector.container.d/
```

Regenerate the service and restart it:

```shell
sudo systemctl daemon-reload
```

```shell
sudo systemctl restart flightctl-catalog-collector
```

Submit a snapshot:

```shell
curl -fsS -i -X POST http://127.0.0.1:8080/v1/snapshots \
  -H 'Content-Type: application/json' \
  -d '{"revision":"1","catalogs":[],"catalogItems":[]}'
```

A successful request returns `204 No Content`. The debug destination logs the revision and resource counts:

```shell
sudo journalctl -u flightctl-catalog-collector -n 50
```

The request also updates the collector's source and pipeline metrics.

The HTTP listener binds `0.0.0.0` inside the container. The drop-in publishes it only on `127.0.0.1` on the host.

The HTTP source has no inbound authentication. Keep the supplied mapping bound to loopback. For remote submissions, restrict network access and use an authenticating proxy with TLS.

## Health and metrics

The unit publishes these host endpoints on loopback:

| Endpoint | URL |
|---|---|
| Liveness | `http://127.0.0.1:13133/livez` |
| Readiness | `http://127.0.0.1:13133/readyz` |
| Metrics | `http://127.0.0.1:8888/metrics` |

Check readiness:

```shell
curl -fsS http://127.0.0.1:13133/readyz
```

Inspect collector operational metrics:

```shell
curl -fsS http://127.0.0.1:8888/metrics \
  | grep '^flightctl_catalogcollector_'
```

Port publishing does not enable these servers. The collector configuration must include the health extension and metrics settings.

Both shipped examples configure:

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

Binding only to loopback inside the container does not make these endpoints reachable through the supplied published-port mappings.

Readiness confirms completion of the collector's startup checks and source startup. It does not confirm a successful catalog import.

## Lifecycle

### Startup and restarts

The unit configures:

```ini
[Unit]
StartLimitIntervalSec=300
StartLimitBurst=5

[Service]
Restart=always
RestartSec=30
```

Systemd restarts the service after process exits, with a 30-second delay. Start attempts are limited to five within a 300-second interval. Reaching that limit stops automatic restart attempts.

Runtime collection and downstream failures in a polling source normally use the collector's retry backoff. Systemd restart handling applies when startup or the process itself fails.

Inspect failures:

```shell
sudo systemctl status flightctl-catalog-collector
```

```shell
sudo journalctl -u flightctl-catalog-collector -n 50
```

After correcting the cause, clear the failure state and start counter:

```shell
sudo systemctl reset-failed flightctl-catalog-collector
```

```shell
sudo systemctl start flightctl-catalog-collector
```

### Behavior at boot

The Quadlet's `[Install]` section contains:

```ini
WantedBy=multi-user.target
```

The generator applies this when producing the service, arranging startup at boot. A separate `systemctl enable` command is not required for this generated unit.

If the configuration file is absent, the startup condition skips the service.

The unit requests `network-online.target` and starts after it. This ordering does not guarantee that the registry or Flight Control API is reachable; startup preflight checks and runtime error handling still apply.

### Applying configuration changes

The collector loads its YAML configuration during startup. Restart after changing it:

```shell
sudoedit /etc/flightctl/flightctl-catalog-collector/config.yaml
```

```shell
sudo systemctl restart flightctl-catalog-collector
```

File-backed authentication has different refresh behavior:

| Change | When it takes effect |
|---|---|
| Bearer token file contents | On the next outgoing request |
| OAuth client ID or secret file contents | When a new access token is acquired |
| Credential file path in YAML | After restarting the collector |
| CA bundle contents or TLS configuration | After restarting the collector |

Restart when an OAuth credential rotation must take effect immediately.

### Customizing the unit

Place overrides under:

```text
/etc/containers/systemd/flightctl-catalog-collector.container.d/
```

After adding or changing a drop-in:

```shell
sudo systemctl daemon-reload
```

```shell
sudo systemctl restart flightctl-catalog-collector
```

Use `[Container]` for Quadlet container settings and `[Service]` for systemd service settings.

### Upgrading

Upgrade the package:

```shell
sudo dnf upgrade flightctl-catalog-collector
```

The package updates the vendor Quadlet and uses RPM systemd scriptlets to restart a running service. It also reloads systemd after the transaction so the generator sees the final installed files.

Administrator-supplied configuration and drop-ins are preserved.

The unit uses `Pull=missing`. Podman pulls the referenced image when it is absent from the rootful image store; it does not refresh an image already available under that reference.

Verify the service and image after upgrading:

```shell
sudo systemctl status flightctl-catalog-collector
```

```shell
sudo podman inspect flightctl-catalog-collector \
  --format '{{.ImageName}}'
```

```shell
curl -fsS http://127.0.0.1:13133/readyz
```

### Disconnected hosts and image mirrors

Make the target release's image available before an upgrade restarts the service. It must either be present in the rootful Podman image store or accessible through the configured registry.

When using `flightctl-mirror-images`, include the optional collector image and select the target image tag:

```shell
flightctl-mirror-images \
  --variant community-el9 \
  --tag-override <image_tag> \
  --dest-registry <registry> \
  --include-optional catalog-collector \
  --execute
```

Choose the variant matching your distribution and OS. Use the image tag referenced by the target release's Quadlet.

This adds the collector to the selected variant's image set. It does not rewrite the installed Quadlet's image reference.

Configure Podman registry mirrors, or use an image override pointing to the mirrored reference:

```ini
[Container]
Image=<mirrored_image_reference>
```

If you override `Image`, maintain its version when upgrading; the drop-in takes precedence over the package-managed reference.

For mirror and offline-bundle options, see the [image mirroring tool documentation](https://github.com/flightctl/flightctl/blob/main/scripts/air-gap/README.md).

### Removing the package

Remove the package:

```shell
sudo dnf remove flightctl-catalog-collector
```

The package's removal scriptlets stop the service. Administrator-created configuration, credentials, and drop-ins are not package-managed files and remain available for reuse. RPM may remove an empty package-owned directory.

If you also want to permanently remove those administrator files, first preserve anything you need, then remove the directories:

```shell
sudo rm -rf /etc/flightctl/flightctl-catalog-collector
```

```shell
sudo rm -rf \
  /etc/containers/systemd/flightctl-catalog-collector.container.d
```

```shell
sudo systemctl daemon-reload
```

## Resource usage

Memory requirements depend on the size of the snapshots and the configured pipelines. Collection, processing, and reconciliation can hold resource data and additional copies or lists in memory.

Measure usage with your expected catalog content when sizing the host. For a running container:

```shell
sudo podman stats --no-stream flightctl-catalog-collector
```
