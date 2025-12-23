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

# Build for specific OS
AGENT_OS_ID=cs10-bootc make e2e-agent-images
```

## Build Process

The script is a wrapper that delegates to the modular build system:
1. **Base image**: Built using `scripts/build.sh --base`
2. **Variants and qcow2**: Built using `scripts/build_and_qcow2.sh`

The build process automatically handles different OS flavors (cs9-bootc, cs10-bootc)
and RPM source detection (local, COPR, or Brew registry).

## OS Flavors and Tagging

The build system supports multiple OS flavors defined in `flavors/` directory:

- **cs9-bootc** - Based on CentOS Stream 9 bootc (default)
- **cs10-bootc** - Based on CentOS Stream 10 bootc

### Building Different Flavors

```bash
# Build cs9-bootc images (default)
./scripts/build.sh --base

# Build cs10-bootc images
AGENT_OS_ID=cs10-bootc ./scripts/build.sh --base
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
├── base/                  # Base image Containerfile
├── variants/              # Variant-specific files
│   ├── v2/, v3/, ..., v10/   # Each contains Containerfile and variant-specific files
├── apps/                  # Application images (Containerfile.<app-name>.<version>)
├── common/                # Shared files used by variants/apps
├── flavors/               # OS flavor configurations (.conf files)
│   ├── cs9-bootc.conf
│   └── cs10-bootc.conf
├── scripts/               # Build automation scripts
│   ├── build.sh           # Main build script (base, variants, apps)
│   ├── build_and_qcow2.sh # Orchestrates parallel builds
│   ├── bundle.sh          # Create image bundles
│   ├── qcow2.sh           # Generate QCOW2 disk images
│   └── upload-images.sh   # Upload images to registry
└── create_agent_images.sh # Main wrapper script
```

The images are built using the `Containerfile` files in the respective directories. For functionality or service deployment changes, update the appropriate `base/Containerfile`, `variants/vX/Containerfile`, or create new variants as needed.

## Build Scripts

The `scripts/` directory contains modular build automation:

- **`build.sh`** - Main build script with options: `--base`, `--variants`, `--apps`
- **`build_and_qcow2.sh`** - Orchestrates variants and QCOW2 builds in parallel
- **`bundle.sh`** - Creates tar bundles of built images for distribution
- **`qcow2.sh`** - Generates bootable QCOW2 disk images using bootc-image-builder
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

### Local Usage and Registry Remapping

Images are built locally with the default repository prefix `quay.io/flightctl/flightctl-device`
(configurable via `IMAGE_REPO`). For e2e testing, images are typically uploaded to a local
registry and the `quay.io/flightctl` prefix is remapped to the local registry address.

To configure registry remapping in a QCOW2 image, use `inject_agent_files_into_qcow.sh`:

```bash
./test/scripts/inject_agent_files_into_qcow.sh --registry-addr <host>:5000
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
