# flightctl-catalog-collector (standalone Linux)

Quadlet unit and example configurations for running the Flight Control catalog
collector as a systemd-managed Podman container, shipped by the
`flightctl-catalog-collector` RPM sub-package.

Unlike the units under the other `deploy/podman/` directories, this one is
**not** rendered by `flightctl-standalone render quadlets` and is **not** part
of `flightctl.target`. The collector is an optional add-on that must remain
installable on a host that does not run the Flight Control service itself, so
the RPM installs the unit directly and only substitutes
`@CATALOG_COLLECTOR_IMAGE@` with the version-pinned image. The reference
itself comes from `packaging/images/<dist>/images.yaml`, the same file that
drives the service quadlets, so a build that repoints the registry moves the
collector with everything else.

## Layout once installed

| Path | Contents |
|------|----------|
| `/usr/share/containers/systemd/flightctl-catalog-collector.container` | Quadlet unit |
| `/etc/flightctl/flightctl-catalog-collector/` | Administrator-supplied configuration, credential files, CA bundles. Mounted read-only at `/etc/flightctl/catalog-collector` in the container. |
| `/usr/share/flightctl/flightctl-catalog-collector/examples/` | Example configurations and the port-publishing drop-in |

## Host paths and container paths

The directory on the left is mounted at the directory on the right:

| Host | Container |
|------|-----------|
| `/etc/flightctl/flightctl-catalog-collector/` | `/etc/flightctl/catalog-collector/` |

Every path written **inside** `config.yaml` — `tokenFile`, `clientIdFile`,
`clientSecretFile`, `certificateAuthority` — is a container path. Writing the
host path there is the most common mistake: the collector fails at startup
with a "no such file or directory" error naming a path that plainly exists on
the host.

## Credential file permissions

The image runs as **uid 1001, gid 0**, and the Quadlet unit runs it under
rootful Podman, so those ids are the ids on the host. Mode `0600 root:root`
is therefore *not* readable by the collector, even though root owns the file.
Grant gid 0 instead, which keeps the file unreadable by every other account:

```shell
sudo install -d -m 0750 -o root -g root \
    /etc/flightctl/flightctl-catalog-collector/credentials
sudo install -m 0640 -o root -g root /dev/null \
    /etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token
sudo $EDITOR /etc/flightctl/flightctl-catalog-collector/credentials/model-registry-token
```

CA bundles are not secret; `0644 root:root` is fine for those. Verify what the
container actually sees:

```shell
sudo podman exec flightctl-catalog-collector \
    head -c 1 /etc/flightctl/catalog-collector/credentials/model-registry-token \
    >/dev/null && echo readable
```

Credentials may also be supplied through the environment and referenced in the
configuration as `${env:NAME}`. Use a root-owned `EnvironmentFile` drop-in or
systemd credentials for that; never put a secret in the unit file itself,
which is world-readable.

The Kubernetes equivalents are different, and deliberately so: there a Secret
volume stays owned by `root:root` whatever uid the pod runs as, so it needs
`defaultMode: 0440` and is read through gid 0 — the same group as here, but a
different mode, because no host-side `install` sets the bits. See the
[chart README](../../helm/flightctl-catalog-collector/README.md#file-permissions).

## Getting started

The unit carries
`ConditionPathExists=/etc/flightctl/flightctl-catalog-collector/config.yaml`,
so it stays inactive until a configuration exists. The collector has no usable
default pipeline — it exits with `error: --config is required` — and the
condition turns that into a clean "condition failed" rather than a restart
loop.

```shell
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/config-vanilla.yaml \
        /etc/flightctl/flightctl-catalog-collector/config.yaml
sudo $EDITOR /etc/flightctl/flightctl-catalog-collector/config.yaml
sudo systemctl daemon-reload
sudo systemctl start flightctl-catalog-collector
```

`daemon-reload` is required, not advisory: Quadlet is a systemd generator, so
`flightctl-catalog-collector.service` does not exist until the generators have
run again. Without it `systemctl start` fails with "Unit not found", and
`ConditionPathExists` is only re-evaluated at that point too.

## Examples

| File | Purpose |
|------|---------|
| `examples/config-vanilla.yaml` | HTTP snapshot source to debug destination. No upstream registry, no Flight Control service: a self-contained check that the collector is installed, healthy, and scrapable. |
| `examples/config-rhoai-to-flightctl.yaml` | Kubeflow Model Registry (Red Hat OpenShift AI) to Flight Control: bearer token to the registry, OAuth2 client credentials to Flight Control, custom CA bundles, server-side model and version selection. |
| `examples/vanilla-publish-8080.conf` | Quadlet drop-in that publishes the vanilla example's HTTP listener on `127.0.0.1:8080`. |

### Running the vanilla example end to end

The shipped unit publishes only the health and metrics ports, because a
polling pipeline has no inbound listener. The vanilla example does have one,
so publish it with the drop-in rather than editing the unit — a package
upgrade replaces the unit but leaves drop-ins alone:

```shell
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/config-vanilla.yaml \
        /etc/flightctl/flightctl-catalog-collector/config.yaml
sudo mkdir -p /etc/containers/systemd/flightctl-catalog-collector.container.d
sudo cp /usr/share/flightctl/flightctl-catalog-collector/examples/vanilla-publish-8080.conf \
        /etc/containers/systemd/flightctl-catalog-collector.container.d/
sudo systemctl daemon-reload
sudo systemctl start flightctl-catalog-collector
```

```shell
curl -sS -X POST http://127.0.0.1:8080/v1/snapshots \
     -H 'Content-Type: application/json' \
     -d '{"revision":"1","catalogs":[],"catalogItems":[]}'
```

The POST returns `204 No Content` and the snapshot appears in
`journalctl -u flightctl-catalog-collector` and in the
`flightctl_catalogcollector_pipeline_snapshot_resources_*` metrics.

The listener binds `0.0.0.0` *inside the container*; the host side of the
mapping is pinned to `127.0.0.1`. The HTTP source has no inbound
authentication, so never move that host side to a routable address.

## Health and metrics

The unit publishes both endpoints on loopback only, because neither is
authenticated:

```shell
curl -s http://127.0.0.1:13133/livez
curl -s http://127.0.0.1:13133/readyz
curl -s http://127.0.0.1:8888/metrics
```

Inside the container both must bind `0.0.0.0`
(`extensions.healthcheck.endpoint` and `service.metrics.endpoint`); the
collector's own default of `localhost` is not reachable through a published
port. Both example configurations already do this.

## Lifecycle

### Startup and restarts

`Restart=always` with `RestartSec=30` keeps the collector running through
transient failures: an upstream registry that is briefly unreachable, a
renewed certificate, a restarted Podman.

Restarts are bounded by `StartLimitIntervalSec=300` and `StartLimitBurst=5`.
A configuration the collector rejects — malformed YAML, an unknown component
type, an unreadable credential file — fails identically on every attempt, so
without a bound the unit would retry forever and bury the real error in the
journal. After five failures within five minutes the unit enters `failed` and
stops trying:

```shell
systemctl status flightctl-catalog-collector
journalctl -u flightctl-catalog-collector -n 50
```

The failure latch survives fixing the configuration, so clear it explicitly:

```shell
sudo $EDITOR /etc/flightctl/flightctl-catalog-collector/config.yaml
sudo systemctl reset-failed flightctl-catalog-collector
sudo systemctl start flightctl-catalog-collector
```

### Behaviour at boot

`WantedBy=multi-user.target` in the unit's `[Install]` section makes the
Quadlet generator create the usual wants symlink, so the collector starts at
boot once a configuration exists. With no configuration the unit is skipped
with "Condition check resulted in ... being skipped" — that is the intended
state on a freshly installed host, not an error. `After=network-online.target`
and `Wants=network-online.target` ensure the network is up first, which
matters for a pipeline that polls a remote registry on startup.

### Applying a configuration change

The collector reads its configuration once, at startup. There is no reload
signal:

```shell
sudo $EDITOR /etc/flightctl/flightctl-catalog-collector/config.yaml
sudo systemctl restart flightctl-catalog-collector
```

Credential *files* are read per request, so rotating a token in place does not
need a restart. Changing which file the configuration points at does.

### Upgrading

`dnf upgrade flightctl-catalog-collector` replaces the unit file with one
pinned to the new image tag and restarts the service through
`%systemd_postun_with_restart`. A `%posttrans` scriptlet runs a second
`daemon-reload` after the old package's files are gone, so the generator sees
the final set of Quadlet files.

Your configuration under `/etc/flightctl/flightctl-catalog-collector/` and any
drop-ins under `/etc/containers/systemd/` are untouched. The new image is
pulled on the next start because the unit sets `Pull=missing` and the tag
changed.

On an air-gapped host, mirror the image before upgrading, or the restart fails
with a pull error:

```shell
flightctl-mirror-images --variant community-el9 \
    --dest-registry <registry> --execute \
    --include-optional catalog-collector
```

### Removing

```shell
sudo dnf remove flightctl-catalog-collector
```

`%preun` stops and disables the service. The configuration directory
`/etc/flightctl/flightctl-catalog-collector/` is intentionally left behind
with its contents: it holds administrator-supplied credentials, and deleting
those on a package removal that might be part of an upgrade would be
destructive. Remove it by hand when you are certain:

```shell
sudo rm -rf /etc/flightctl/flightctl-catalog-collector
sudo rm -rf /etc/containers/systemd/flightctl-catalog-collector.container.d
sudo systemctl daemon-reload
```

### Verifying a release

```shell
systemctl is-enabled flightctl-catalog-collector
systemctl is-active flightctl-catalog-collector
systemctl show flightctl-catalog-collector -p Restart -p RestartUSec \
    -p StartLimitIntervalUSec -p StartLimitBurst
podman inspect flightctl-catalog-collector --format '{{.ImageName}}'
curl -s http://127.0.0.1:13133/readyz
```

## Resource usage

The collector is small. A measurement of the el9 image running the vanilla
pipeline, after serving a snapshot, is about 27 MiB RSS and effectively no
steady-state CPU between polls. Peak memory scales with the size of a single
snapshot — the number of models and versions in the upstream registry — not
with how often it polls.
