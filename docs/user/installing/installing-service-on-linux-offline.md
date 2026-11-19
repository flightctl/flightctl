# Installing the Flight Control service offline on Linux

This document describes how to install the Flight Control server on a RHEL machine
that has no internet access. The `flightctl-mirror-images` tool running on a connected prep
machine creates a single portable archive containing all required container images
and optionally the `flightctl-services` RPM. You then transfer the archive to the
air-gapped target and use the included scripts to complete the installation.

For the connected (online) installation procedure, see
[Flight Control quadlet-based installation](installing-service-on-linux.md).

## Prerequisites

On the **prep machine** (internet-connected):

- RHEL 9 or RHEL 10 with `skopeo` and `createrepo_c` installed (`sudo dnf install -y skopeo createrepo_c`)
- The `flightctl-mirror-images` binary built from the flightctl repository (`make build-mirror-images`)
- Sufficient disk space for the bundle (~5–10 GB depending on the variant)

On the **target machine** (air-gapped):

- RHEL 9 or RHEL 10
- `podman` installed
- `skopeo` installed (`skopeo` must be available before network is removed,
  or transferred as an RPM in the bundle)
- `containernetworking-plugins` installed (`sudo dnf install -y containernetworking-plugins`)
- A transfer method — see [Packaging artifacts for portable media](offline-portable-media.md)

## Step 1: Create the offline bundle on the prep machine

Run `flightctl-mirror-images` with the `--bundle` flag. The command downloads all container
images required for your chosen deployment variant and packages them into a single
`.tar.gz` archive. The `--bundle-rpms` flag includes the `flightctl-services` RPM
and its dependencies so you can complete the installation without any network access.
The `--rpm-createrepo` flag generates repository metadata in the bundle so the
install script on the target can install packages by name rather than by file glob
(avoids conflicts with protected system packages).

> [!IMPORTANT]
> The image tags in the bundle **must match** the version expected by the installed
> RPM. Running from a development branch where `appVersion` is `latest` will tag all
> images as `:latest`, but versioned RPMs (e.g. `flightctl-services-1.2.0`) reference
> a specific tag (e.g. `:1.2.0`). Always pin the version — see
> [Pinning to a specific release version](#pinning-to-a-specific-release-version) below.

```bash
./bin/flightctl-mirror-images \
    --variant community-el9 \
    --bundle ~/flightctl-bundle.tar.gz \
    --bundle-rpms \
    --rpm-createrepo \
    --rpm-exclude flightctl-agent
```

The `--rpm-exclude flightctl-agent` flag downloads the agent RPM into the bundle's
`rpms/` directory (for use when building device OS images with the image builder) but
does not auto-install it on the server. Omit this flag only if you intentionally want
the agent installed on the server machine.

Replace `community-el9` with your target variant:

| Variant | Use when |
|---------|----------|
| `community-el9` | RHEL 9 or CentOS Stream 9 |
| `community-el10` | RHEL 10 or CentOS Stream 10 |
| `rhem-el9` | RHEL 9 with Red Hat registry images (requires registry.redhat.io credentials) |
| `rhem-el10` | RHEL 10 with Red Hat registry images |

### Pinning to a specific release version

By default, `flightctl-mirror-images` reads the `appVersion` field from `Chart.yaml` in your
current checkout to determine which image tags to pull. The RPM download fetches the
latest version available in the FlightCtl repository. To produce a bundle for a
specific stable release, use one of the following approaches.

#### Recommended: check out the release tag

Check out the release tag before building the tool and running the command. This
automatically sets `appVersion` to the release version so no extra flags are needed:

```bash
git checkout v1.1.2
make build-mirror-images
./bin/flightctl-mirror-images \
    --variant community-el9 \
    --bundle ~/flightctl-bundle-1.1.2.tar.gz \
    --bundle-rpms
```

#### Alternative: use flag overrides without switching branches

If you cannot check out the release tag, pass `--tag-override` to pin the container
image tags. The tool automatically pins the RPM version to match — converting the
image tag format (`1.1.2`) to the RPM version format (`1.1.2`) — so both the
bundled images and the installed RPM reference the same version:

```bash
./bin/flightctl-mirror-images \
    --variant community-el9 \
    --bundle ~/flightctl-bundle-1.1.2.tar.gz \
    --bundle-rpms \
    --tag-override 1.1.2
```

To see which RPM versions are available in the FlightCtl repository before running
the command:

```bash
dnf list --showduplicates flightctl-services
```

The command creates:

```console
~/flightctl-bundle.tar.gz
├── images/          — container images in skopeo dir: format
├── rpms/            — flightctl-services RPM and dependencies
├── import.sh        — imports images into a local registry
└── install-rpms.sh  — installs RPMs with dnf
```

## Step 2: Transfer the bundle to the target machine

Transfer `~/flightctl-bundle.tar.gz` to the air-gapped target using your available
method. See [Packaging artifacts for portable media](offline-portable-media.md) for
USB drive and other transfer formats. If the target is reachable via a jump host:

```bash
scp ~/flightctl-bundle.tar.gz <user>@<target_host>:~/
```

## Step 3: Extract the bundle

Extract the bundle into a working directory on the target machine:

```bash
mkdir ~/flightctl-bundle
tar -xzf ~/flightctl-bundle.tar.gz -C ~/flightctl-bundle
```

## Step 4: Start a local container registry on the target machine

The Flight Control quadlet services pull their images at startup. On an air-gapped
machine those pulls must come from a local registry. The bundle includes
`docker.io/library/registry:2` so you do not need internet access.

Load the `registry:2` image from the bundle into the root user's podman image
storage so the registry container runs as a system service:

```bash
sudo skopeo copy \
    "dir:$HOME/flightctl-bundle/images/library/registry:2" \
    "containers-storage:docker.io/library/registry:2"
```

Create the registry data directory and start the registry container as root:

```bash
sudo mkdir -p /var/lib/local-registry
sudo podman run -d --name local-registry \
    --network=host \
    --security-opt label=disable \
    -v /var/lib/local-registry:/var/lib/registry:Z \
    --restart=always \
    docker.io/library/registry:2
```

Enable `podman-restart.service` so the registry container starts automatically
after a reboot:

```bash
sudo systemctl enable podman-restart.service
```

Verify the registry is ready:

```bash
curl http://localhost:5000/v2/
```

```console
{}
```

> [!IMPORTANT]
> Running the registry as root (with `sudo podman`) and enabling
> `podman-restart.service` ensures it persists across logouts and reboots.
> Without additional configuration such as lingering sessions, a rootless
> container stops when the owning user session ends.

## Step 5: Import images into the local registry

Run the included import script as root to push all bundled images into the local
registry. Running as root causes `skopeo` to write detached image signatures to
the default system-wide directory (`/var/lib/containers/sigstore/`). If your
system has a custom `lookaside-staging` path configured in `registries.d`, the
signatures are written there instead:

```bash
cd ~/flightctl-bundle
sudo ./import.sh
```

By default `import.sh` targets `localhost:5000`. To use a different registry address:

```bash
sudo ./import.sh --registry <host>:<port>
```

If your bundle includes images from `registry.redhat.io` or
`registry.access.redhat.com`, complete the steps in
[Configure offline signature lookup](#configure-offline-signature-lookup) before
starting the services.

Verify the images are available:

```bash
curl http://localhost:5000/v2/_catalog
```

## Step 6: Install the flightctl-services RPM

Run the included RPM installation script from the extracted bundle directory:

```bash
cd ~/flightctl-bundle
./install-rpms.sh
```

The script installs `flightctl-services` and all bundled dependencies using
`sudo dnf install`. It uses `--nobest` so that system packages already present
on the target (such as `librepo`) are used at their installed version rather
than requiring the exact version from the bundle. This makes bundles portable
across RHEL minor versions — a bundle built on RHEL 9.8 installs cleanly on a
RHEL 9.5 target without version conflicts.

## Step 7: Configure image registry redirection

The quadlet unit files installed by `flightctl-services` reference images on their
original upstream registries (for example `quay.io/flightctl/flightctl-api-el9`).
You must configure the system to redirect those pulls to your local registry.

Edit `/etc/containers/registries.conf` and add mirror entries for each source
registry used by your variant:

```toml
[[registry]]
prefix = "quay.io"
location = "localhost:5000"
insecure = true

[[registry]]
prefix = "docker.io"
location = "localhost:5000"
insecure = true

[[registry]]
prefix = "registry.access.redhat.com"
location = "localhost:5000"
insecure = true
```

For `rhem-el9` or `rhem-el10` variants, add a mirror entry for
`registry.redhat.io` in place of `registry.access.redhat.com`.

> [!IMPORTANT]
> The `insecure = true` flag is required when the local registry serves plain HTTP
> (which is the default for the registry container started in Step 4). If your
> registry is configured with TLS, remove this flag and ensure its CA certificate
> is trusted on the target system.

### Configure offline signature lookup

RHEL's container policy requires signature verification for images from
`registry.redhat.io` and `registry.access.redhat.com`. The `import.sh` script
in Step 5 wrote detached signatures to `/var/lib/containers/sigstore/` (the
default system-wide signature directory). You must configure `registries.d` so
that podman reads signatures from that directory instead of the internet.

Because the registry redirect in Step 7 routes pulls from the original source
registries to the local registry via `registries.conf`, podman resolves the
signature lookaside using the **physical import destination** (`localhost:5000`
by default) rather than the original source registry names. Configure a single
lookaside entry for the import destination.

Create or edit a file under `/etc/containers/registries.d/` — the
filename is arbitrary (any `.yaml` name works); what matters is the
`docker` namespace key matching your import destination:

```yaml
# /etc/containers/registries.d/flightctl-offline.yaml
docker:
  "localhost:5000":
    lookaside: file:///var/lib/containers/sigstore
```

If you used `--registry <host>:<port>` during import, replace
`localhost:5000` with your actual destination registry.

If a `registries.d` file already contains a `docker` section for your
import destination, merge the `lookaside` key into that existing section.
Preserve any other keys in that section (such as `lookaside-staging` or
`use-sigstore-attachments`). Do not duplicate the same `docker` namespace
across multiple `registries.d` files.

> [!NOTE]
> If your system uses a custom signature storage path (configured via
> `lookaside-staging` or a non-default `lookaside` in `registries.d`), use
> the actual path where `skopeo` wrote the signatures instead of the default
> `/var/lib/containers/sigstore`.

Verify the configuration by pulling one of the images included in your bundle.
To find the original source reference and tag for a bundled image, check the
`import.sh` script — each `skopeo copy` line shows the image path. For example,
if the script contains `docker://$REGISTRY/rhel9/postgresql-16:9.8`, the
original source reference is `registry.redhat.io/rhel9/postgresql-16:9.8`:

```bash
sudo podman pull --log-level=debug <source_registry>/<image_path>:<tag>
```

Replace `<source_registry>/<image_path>:<tag>` with an image and tag from your
bundle's `import.sh`. The pull should succeed with the registry redirect
resolving to `localhost:5000` and signature verification completing against the
local directory. If it fails, review the full output for `lookaside` or
`sigstore` path messages that indicate where podman looked for signatures.

> [!IMPORTANT]
> Do not remove or weaken the signature verification policy in
> `/etc/containers/policy.json`. The steps above configure podman to find the
> bundled signatures locally so that verification succeeds offline.

## Step 8: Configure and start the Flight Control services

Configure the Flight Control service by creating or editing
`/etc/flightctl/service-config.yaml`. A minimal working configuration requires
three fields:

```yaml
global:
  baseDomain: my-server.example.com  # fully qualified domain name — not an IP address
  generateCertificates: builtin       # generate a self-signed CA automatically
  auth:
    type: oidc                        # use the built-in PAM OIDC issuer
```

| Field | Description |
|-------|-------------|
| `global.baseDomain` | The FQDN from which service endpoints are derived (e.g. `api.my-server.example.com`). Must resolve via DNS — raw IP addresses are not supported. If omitted, defaults to the output of `hostname -f`. |
| `global.generateCertificates` | `builtin` generates a self-signed CA and all service certificates automatically. Use `none` only if you supply your own certificates in `/etc/flightctl/pki/`. |
| `global.auth.type` | Authentication backend. Valid values: `oidc` (uses the built-in PAM issuer installed with `flightctl-services`), `aap` (Ansible Automation Platform), `oauth2`, or `none` (disables authentication — not recommended for production). **Note:** `pam` is not a valid value — use `oidc` to enable PAM-based authentication via the built-in issuer. |

For a full list of configuration options, see
[Installing the Flight Control Service on Linux](installing-service-on-linux.md#installing-the-rpm).

Start the Flight Control services:

```bash
sudo systemctl enable --now flightctl.target
```

Monitor that all services come up:

```bash
sudo systemctl list-units flightctl-*.service
```

```bash
sudo podman ps
```

## Verifying the installation

Check that all quadlet services are running:

```bash
sudo systemctl status flightctl.target
```

Confirm the API is reachable using the FQDN you set in `global.baseDomain`.
The default API port is `3443`; installations using a reverse proxy or load
balancer may use port `443` instead:

```bash
curl -k https://<baseDomain>:3443/api/v1/fleets
```

## Optional: deploying the observability stack offline

The Prometheus and Grafana container images are **not included** in the standard
bundle. If you need the `flightctl-observability` stack (metrics dashboards and
alerting), you must mirror those images manually and include the RPM in the bundle.

See [Deploying Observability Offline](deploying-observability-linux.md#air-gapped-installation)
for the complete procedure, including image lists, `skopeo` commands, and
configuration notes.

## flightctl-mirror-images flag reference

| Flag | Default | Description |
|------|---------|-------------|
| `--variant` | — | Deployment variant: `community-el9`, `community-el10`, `rhem-el9`, `rhem-el10`. Required unless `--agent-only` is set. |
| `--dest-registry` | `localhost:5000` (bundle mode) | Destination registry `host:port` — no scheme. Required in live-push mode. |
| `--execute` | `false` | Execute skopeo copy commands immediately. Mutually exclusive with `--bundle`. |
| `--insecure` | `false` | Disable TLS verification for the destination registry. Required for plain HTTP registries. |
| `--tag-override` | — | Pin the image tag for all untagged FlightCtl images (e.g. `1.2.0`). Also pins the RPM version automatically when `--bundle-rpms` or `--agent-only` is set. Third-party images with explicit tags are unaffected. |
| `--bundle` | — | Create a self-contained `.tar.gz` archive at the given path. Mutually exclusive with `--execute`. |
| `--bundle-rpms` | `false` | Include RPMs and an `install-rpms.sh` script in the bundle. Requires `--bundle`. |
| `--rpm-packages` | `flightctl-services,`<br>`flightctl-cli,`<br>`flightctl-agent` | Comma-separated list of RPM packages to download into the bundle. |
| `--rpm-exclude` | — | Comma-separated packages to download but skip during auto-install. RPMs remain in `rpms/` for manual use (e.g. embedding `flightctl-agent` into device OS images). |
| `--rpm-repo-url` | `https://rpm.flightctl.io/`<br>`flightctl-epel.repo` | URL of the `.repo` file used for RPM downloads. Override to use COPR or a custom repo. |
| `--rpm-createrepo` | `false` | Run `createrepo_c` after `dnf download` to generate `repodata/` in the bundle. Prevents `dnf` protected-package conflicts on install. Mutually exclusive with `--rpm-reposync`. |
| `--rpm-reposync` | `false` | Mirror the full FlightCtl RPM repo using `dnf reposync`, including repodata. Requires `dnf-plugins-core`. Mutually exclusive with `--rpm-createrepo`. |
| `--agent-only` | `false` | RPM-only bundle for edge device installation — skips all image bundling, `--variant` not required. Defaults `--rpm-packages` to `flightctl-agent,flightctl-cli,open-vm-tools,ignition,afterburn,cloud-init`. |

## Next steps

- [Configuring Authentication and Authorization](configuring-auth/overview.md) —
  configure an authentication provider before allowing user access
- [Installing the Flight Control CLI](installing-cli.md) — install the CLI to
  manage the service
- [Deploying an Observability Stack on RHEL](deploying-observability-linux.md) —
  Prometheus and Grafana installation and configuration
- [Flight Control quadlet-based installation](installing-service-on-linux.md) —
  full configuration reference for the online installation path
