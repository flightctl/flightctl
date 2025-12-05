# E2E Agent images

We generate multiple agent images for testing purposes, each with a different
services running, but all connected to our flightctl service for management.

This work is performed by the `create_agent_images.sh` script in this
directory.

And can be triggered from the top-level makefile with: `make e2e-agent-images`

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

```
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

| Name   | Image                         | Bootc Containers                 |
|------  |-------------------------------|----------------------------------|
| base   | `bin/output/qcow2/disk.qcow2` | ${IP}:5000/flightctl-device:base |
| v2     | N/A                           | $(IP):5000/flightctl-device:v2   |
| v3     | N/A                           | $(IP):5000/flightctl-device:v3   |

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
