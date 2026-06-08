# Installing the Flight Control service offline on Linux

This document describes how to install the Flight Control server on a RHEL machine
that has no internet access. The `mirror-images` tool running on a connected prep
machine creates a single portable archive containing all required container images
and optionally the `flightctl-services` RPM. You then transfer the archive to the
air-gapped target and use the included scripts to complete the installation.

For the connected (online) installation procedure, see
[Flight Control quadlet-based installation](installing-service-on-linux.md).

## Prerequisites

On the **prep machine** (internet-connected):

- RHEL 9 or RHEL 10 with `skopeo` installed (`sudo dnf install -y skopeo`)
- The `mirror-images` binary built from the flightctl repository (`make build-mirror-images`)
- Sufficient disk space for the bundle (~5–10 GB depending on the variant)

On the **target machine** (air-gapped):

- RHEL 9 or RHEL 10
- `podman` installed
- `skopeo` installed (`skopeo` must be available before network is removed,
  or transferred as an RPM in the bundle)
- `containernetworking-plugins` installed (`sudo dnf install -y containernetworking-plugins`)
- A transfer method — see [Packaging artifacts for portable media](offline-portable-media.md)

## Step 1: Create the offline bundle on the prep machine

Run `mirror-images` with the `--bundle` flag. The command downloads all container
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
./bin/mirror-images \
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

By default, `mirror-images` reads the `appVersion` field from `Chart.yaml` in your
current checkout to determine which image tags to pull. The RPM download fetches the
latest version available in the FlightCtl repository. To produce a bundle for a
specific stable release, use one of the following approaches.

#### Recommended: check out the release tag

Check out the release tag before building the tool and running the command. This
automatically sets `appVersion` to the release version so no extra flags are needed:

```bash
git checkout v1.1.2
make build-mirror-images
./bin/mirror-images \
    --variant community-el9 \
    --bundle ~/flightctl-bundle-1.1.2.tar.gz \
    --bundle-rpms
```

#### Alternative: use flag overrides without switching branches

If you cannot check out the release tag, pass `--tag-override` to pin the container
image tags and include the version in `--rpm-packages` to pin the RPM:

```bash
./bin/mirror-images \
    --variant community-el9 \
    --bundle ~/flightctl-bundle-1.1.2.tar.gz \
    --bundle-rpms \
    --tag-override v1.1.2 \
    --rpm-packages flightctl-services-1.1.2
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

Load the `registry:2` image from the bundle into podman's local image storage:

```bash
skopeo copy \
    "dir:$HOME/flightctl-bundle/images/library/registry:2" \
    "containers-storage:docker.io/library/registry:2"
```

Create the registry data directory and start the registry container:

```bash
mkdir -p ~/registry-data
podman run -d --name local-registry \
    --network=host \
    --security-opt label=disable \
    -v ~/registry-data:/var/lib/registry \
    --restart=always \
    docker.io/library/registry:2
```

Verify the registry is ready:

```bash
curl http://localhost:5000/v2/
```

```console
{}
```

> [!NOTE]
> `--network=host` avoids port-forwarding issues with rootless podman on RHEL 9.
> Use `--security-opt label=disable` in place of the `:z` volume flag when
> `--network=host` is set.

## Step 5: Import images into the local registry

Run the included import script to push all bundled images into the local registry:

```bash
cd ~/flightctl-bundle
./import.sh
```

By default `import.sh` targets `localhost:5000`. To use a different registry address:

```bash
./import.sh --registry <host>:<port>
```

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
`sudo dnf install`.

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

Confirm the API is reachable using the FQDN you set in `global.baseDomain`:

```bash
flightctl get fleets
```

## Optional: deploying the observability stack offline

The Prometheus and Grafana container images are **not included** in the standard
bundle. If you need the `flightctl-observability` stack (metrics dashboards and
alerting), you must mirror those images manually and include the RPM in the bundle.

See [Deploying Observability Offline](deploying-observability-linux.md#air-gapped-installation)
for the complete procedure, including image lists, `skopeo` commands, and
configuration notes.

## Next steps

- [Configuring Authentication and Authorization](configuring-auth/overview.md) —
  configure an authentication provider before allowing user access
- [Installing the Flight Control CLI](installing-cli.md) — install the CLI to
  manage the service
- [Deploying an Observability Stack on RHEL](deploying-observability-linux.md) —
  Prometheus and Grafana installation and configuration
- [Flight Control quadlet-based installation](installing-service-on-linux.md) —
  full configuration reference for the online installation path
