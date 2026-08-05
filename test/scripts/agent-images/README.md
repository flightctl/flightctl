# E2E Agent images

We generate multiple agent images for testing purposes, each with a different
services running, but all connected to our flightctl service for management.

This work is performed by the `create_agent_images.sh` script in this
directory.

And can be triggered from the top-level makefile with: `make e2e-agent-images`

The `AGENT_OS_ID` parameter controls which OS flavor to build:

```bash
# Build for default OS (cs9-bootc)
make e2e-agent-images

# Build package-mode images
BUILD_TYPE=regular AGENT_OS_ID=cs9-regular make e2e-agent-images

# Build for specific OS
AGENT_OS_ID=cs10-bootc make e2e-agent-images
```

## Build Process

The script is a wrapper that delegates to the modular build system:
1. **Base image**: Built using `scripts/build.sh --base`
2. **Bootc path**: `scripts/build_and_qcow2.sh` builds variants and a bootc qcow2
3. **Package-mode path**: `BUILD_TYPE=regular AGENT_OS_ID=cs9-regular` builds the package-mode base OCI image only (`SKIP_QCOW_BUILD=true` by default; package-mode qcow2 is unsupported)

The build process automatically handles different OS flavors (cs9-bootc, cs9-regular, cs10-bootc)
and RPM source detection (local, COPR, or Brew registry).

`BUILD_TYPE=regular` is a package-mode-only path in `create_agent_images.sh`. There,
omitting `AGENT_OS_ID` defaults it to `cs9-regular`, and any other `AGENT_OS_ID`
with `BUILD_TYPE=regular` fails fast.

For the `make e2e-agent-images` entrypoint, pass `AGENT_OS_ID=cs9-regular`
explicitly. Make still defaults `AGENT_OS_ID` to `cs9-bootc`.

## OS Flavors and Tagging

The build system supports multiple OS flavors with dedicated Containerfiles:

- **cs9-bootc** - Based on CentOS Stream 9 bootc (default)
- **cs9-regular** - Package-mode base for non-bootc E2E coverage
- **cs10-bootc** - Based on CentOS Stream 10 bootc

Each flavor has its own explicit Containerfile eliminating conditional logic.

### Building Different Flavors

```bash
# Build cs9-bootc images (default, community)
./scripts/build.sh --base

# Build cs9-regular package-mode images
AGENT_OS_ID=cs9-regular ./scripts/build.sh --base

# Build cs10-bootc images (community)
AGENT_OS_ID=cs10-bootc ./scripts/build.sh --base

# Build Red Hat variants
DISTRO=redhat AGENT_OS_ID=cs9-bootc ./scripts/build.sh --base
DISTRO=redhat AGENT_OS_ID=cs10-bootc ./scripts/build.sh --base

# cs9-regular package-mode does not support DISTRO=redhat/RHEM
```

### Image Tagging

Images are tagged with OS flavor identifiers for easy selection:

**Base Images:**
- `quay.io/flightctl/flightctl-device:base-cs9-bootc-${TAG}` (canonical)
- `quay.io/flightctl/flightctl-device:base-cs10-bootc-${TAG}` (canonical)
- `quay.io/flightctl/flightctl-device:base` (latest flavor)
- `quay.io/flightctl/flightctl-device:base-cs9-bootc`
- `quay.io/flightctl/flightctl-device:base-${TAG}`

**Variant Images:**
- `quay.io/flightctl/flightctl-device:v2-cs9-bootc-${TAG}`
- `quay.io/flightctl/flightctl-device:v2-cs10-bootc-${TAG}`
- `quay.io/flightctl/flightctl-device:v2` (latest flavor)
- `quay.io/flightctl/flightctl-device:v2-cs9-bootc`

This allows selecting specific OS versions in deployment configurations.

## Directory Structure

The build system now uses a modular structure:

```text
agent-images/
├── base/                  # Shared files for base images
├── containerfiles/        # OS flavor-specific Containerfiles
│   ├── cs9-bootc/         # CentOS Stream 9 bootc
│   │   └── Containerfile
│   ├── cs9-regular/       # Package-mode base image
│   │   └── Containerfile
│   ├── cs9-bootc-redhat/  # RHEL 9 bootc
│   │   └── Containerfile
│   ├── cs10-bootc/        # CentOS Stream 10 bootc
│   │   └── Containerfile
│   └── cs10-bootc-redhat/ # RHEL 10 bootc
│       └── Containerfile
├── variants/              # Variant-specific files
│   ├── v2/, v3/, ..., v10/   # Each contains Containerfile and variant-specific files
├── apps/                  # Application images (Containerfile.<app-name>.<version>)
├── common/                # Shared files used by variants/apps
├── scripts/               # Build automation scripts
│   ├── build.sh           # Main build script (base, variants, apps)
│   ├── build_and_qcow2.sh # Orchestrates parallel builds
│   ├── bundle.sh          # Create image bundles
│   ├── qcow2.sh           # Generate QCOW2 disk images
│   └── upload-images.sh   # Upload images to registry
└── create_agent_images.sh # Main wrapper script
```

The images are built using the `Containerfile` files in the respective directories. For functionality or service deployment changes, update the appropriate `containerfiles/*/Containerfile`, `variants/vX/Containerfile`, or create new variants as needed.

## Build Scripts

The `scripts/` directory contains modular build automation:

- **`build.sh`** - Main build script with options: `--base`, `--variants`, `--apps`
- **`build_and_qcow2.sh`** - Orchestrates variant and QCOW2 builds; for `*-regular` it is QCOW2-only by default unless `SKIP_VARIANTS_BUILD=false` is set explicitly
- **`bundle.sh`** - Creates tar bundles of built images for distribution
- **`qcow2.sh`** - Generates bootable QCOW2 disk images using bootc-image-builder
- **`qcow2_regular.sh`** - Generates package-mode QCOW2 images from a bootable cloud image via host-side `dnf --installroot` plus `virt-customize` post-config
- **`upload-images.sh`** - Uploads image bundles to container registries

Use `./scripts/build.sh --help` for detailed usage and options.

### Image Tagging

Each image is tagged with multiple tags for flexibility:

| Tag Pattern               | Example                                             |
|---------------------------|-----------------------------------------------------|
| `<name>-${OS_ID}-${TAG}`  | `quay.io/flightctl/flightctl-device:base-cs9-bootc-v0.5.0` |
| `<name>`                  | `quay.io/flightctl/flightctl-device:base`           |
| `<name>-${OS_ID}`         | `quay.io/flightctl/flightctl-device:base-cs9-bootc` |
| `<name>-${TAG}`           | `quay.io/flightctl/flightctl-device:base-v0.5.0`    |

Where `<name>` is `base`, `v2`, `v3`, etc.

### Build Outputs

| Name   | QCOW2 Image                      | Container Image Tags                        |
|--------|----------------------------------|---------------------------------------------|
| base   | `bin/output/qcow2/disk.qcow2`    | `base`, `base-${OS_ID}`, `base-${TAG}`, `base-${OS_ID}-${TAG}` |
| v2     | N/A                              | `v2`, `v2-${OS_ID}`, `v2-${TAG}`, `v2-${OS_ID}-${TAG}` |
| v3     | N/A                              | `v3`, `v3-${OS_ID}`, `v3-${TAG}`, `v3-${OS_ID}-${TAG}` |

> **Note:** `qcow2.sh` writes the disk image to `bin/output/agent-qcow2-${OS_ID}/qcow2/disk.qcow2`.
> When using `create_agent_images.sh`, the image is moved to `bin/output/qcow2/disk.qcow2`.

For `BUILD_TYPE=regular AGENT_OS_ID=cs9-regular`, `create_agent_images.sh` still writes the final QCOW2 to the same `bin/output/qcow2/disk.qcow2` path, but it is produced by `scripts/qcow2_regular.sh` from the configured cloud image instead of `bootc-image-builder`.

That means the package-mode OCI base and package-mode qcow2 are parallel build paths:
- `containerfiles/cs9-regular/Containerfile` is the source of truth for the OCI base image
- `scripts/qcow2_regular.sh` is the source of truth for the package-mode qcow2 disk customization

They are intentionally parallel because the qcow2 must start from a **bootable** CentOS
GenericCloud image (kernel/bootloader). The OCI image is a container rootfs and is not
exported as the disk.

`qcow2_regular.sh` mounts that cloud disk on the host (`guestmount`) and installs packages
via `dnf --installroot` inside a CentOS Stream 9 container (runner network, not libguestfs
guest networking). Flightctl RPMs use `tsflags=noscripts` — agent/selinux `%post` is
unreliable under guestmount FUSE. After unmount, `virt-customize` loads the
`flightctl_agent` SELinux module (build fails if it is missing), finishes user/linger
setup, and runs `--selinux-relabel`. Guest `dnf` inside `virt-customize` is avoided.

Agent config, certificates, and registry remapping are expected to be injected into the
qcow2 after build via the existing e2e injection flow.

### Local Usage and Registry Remapping

Images are built locally with the default repository prefix `quay.io/flightctl/flightctl-device`
(configurable via `IMAGE_REPO`). For e2e testing, images are typically uploaded to a local
registry and the `quay.io/flightctl` prefix is remapped to the local registry address.

To configure registry remapping in a QCOW2 image, use `inject_agent_files_into_qcow.sh`:

```bash
./test/scripts/inject_agent_files_into_qcow.sh --registry-address <host>:5000
```

This creates a containers registry config at `/etc/containers/registries.conf.d/flightctl-remap.conf`
that remaps `quay.io/flightctl` to the local registry:

```toml
[[registry]]
prefix = "quay.io/flightctl"
location = "<host>:5000/flightctl"
```

With this config, when the agent pulls `quay.io/flightctl/flightctl-device:v2`, it will
actually pull from `<host>:5000/flightctl/flightctl-device:v2`.

## Credentials

All images are built with the following credentials:
- user: `user`
- password: `user`

## Image descriptions
### base
This image is the base image for all other images. It contains the following services:
- `flightctl-agent` - The agent service that connects to the flightctl service configured
   with the `test/script/prepare_agent_config.sh` script to be connected to our local
   flightctl service.

The installed flightctl-agent will be either a locally compiled rpm or a downloaded
rpm based on the `FLIGHTCTL_RPM` variable, please see [test-docs](../../README.md) for more information.

It is configured to trust our locally generated CA created in `test/scripts/create_e2e_certs.sh`

### v2
This image builds on top of the base image, and adds the following services, useful
to test agent reporting of systemd services:
 * test-e2e-dummy which just runs a sleep 3600 for 1h
 * test-e2e-crashing which runs /bin/false and attempts restart every few minutes

### v3
This image builds on top of the base image, and adds the following services, useful
 * test-e2e-another-dummy which just runs a sleep 3600 for 1h
