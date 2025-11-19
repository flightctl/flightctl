# FlightCtl Device Image Build Scripts

This directory contains scripts for building FlightCtl device images, variants, and applications.

## Overview

The build system creates bootable device images based on bootc, with multiple variants for testing different configurations. It uses podman/buildah with BuildKit features like named build contexts and build argument files.

## Directory Structure

```
agent-images/
├── base/                  # Base image Containerfile
├── variants/              # Variant-specific files
│   ├── v2/, v3/, ..., v10/
│   └── Each contains Containerfile and variant-specific files
├── apps/                  # Application images
│   └── Containerfile.<app-name>.<version>
├── common/                # Shared files used by variants/apps
├── flavors/               # Flavor configurations (.conf files)
│   ├── cs9-bootc.conf
│   └── cs10-bootc.conf
└── scripts/               # Build automation (this directory)
    ├── build.sh           # Main build script
    ├── bundle.sh          # Create image bundles
    ├── qcow2.sh           # Generate QCOW2 disk images
    └── build_and_qcow2.sh # Orchestrates parallel builds
```

## Scripts

### `build.sh` - Main Build Script

The primary script for building base images, variants, and apps.

**Usage:**
```bash
./build.sh [OPTIONS]

Options:
  --base      Build base images
  --variants  Build variant images
  --apps      Build application images
  --jobs N    Number of parallel jobs (default: nproc)
```

**Examples:**
```bash
# Build only base (default if no options)
./build.sh

# Build base explicitly
./build.sh --base

# Build base and variants
./build.sh --base --variants

# Build everything with 4 parallel jobs
./build.sh --base --variants --apps --jobs 4
```

**Environment Variables:**
- `TAG` - Image tag (default: `latest`)
- `IMAGE_REPO` - Device image repository (default: `quay.io/flightctl/flightctl-device`)
- `APP_REPO` - Application image repository (default: `quay.io/flightctl`)
- `FLAVORS` - Space-separated list of flavors (default: `cs9-bootc cs10-bootc`)
- `VARIANTS` - Space-separated list of variants (default: `v2 v3 v4 v5 v6 v7 v8 v9 v10`)
- `PODMAN_BUILD_EXTRA_FLAGS` - Additional flags for podman build

**How it works:**

1. **Base Images**: For each flavor, reads `flavors/<flavor>.conf` and builds base image using:
   - Build arguments from flavor conf file via `--build-arg-file`
   - Git metadata as individual build args
   - Named build context `project-root` for accessing project files

2. **Variant Images**: For each variant, builds on top of base image using:
   - Build context `variant-context` pointing to `variants/v{N}/`
   - Build context `common` pointing to `common/`
   - Build context `project-root` for project files
   - Parallel builds using xargs with `-P ${JOBS}`

3. **App Images**: Auto-detects `Containerfile.<app-name>.<version>` files in `apps/`:
   - Extracts app name and version from filename
   - Uses `common` build context
   - Tags as `${APP_REPO}/${app-name}:${version}-${TAG}`

### `bundle.sh` - Image Bundle Creator

Creates tar archives of built images for distribution.

**Usage:**
```bash
./bundle.sh

Environment Variables:
  TAG       - Image tag
  OS_ID     - OS identifier (e.g., cs9-bootc)
  IMAGE_REPO - Image repository
  VARIANTS  - Variants to include
```

**Output:**
Creates `artifacts/agent-images-bundle-${OS_ID}-${TAG}.tar` containing all variant images.

### `qcow2.sh` - QCOW2 Image Generator

Generates bootable QCOW2 disk images from container images using bootc-image-builder.

**Usage:**
```bash
./qcow2.sh

Environment Variables:
  TAG       - Image tag
  OS_ID     - OS identifier
  IMAGE_REPO - Image repository
```

**Output:**
Creates `artifacts/flightctl-device-${OS_ID}-${TAG}.qcow2` disk image.

### `build_and_qcow2.sh` - Parallel Build Orchestrator

Orchestrates variant building and QCOW2 generation in parallel for a single flavor.

**Usage:**
```bash
./build_and_qcow2.sh [--os-id OS_ID]

# Or via environment:
OS_ID=cs9-bootc TAG=v1.0 ./build_and_qcow2.sh
```

**What it does:**
1. Builds variants and creates bundle (parallel job 1)
2. Builds QCOW2 image (parallel job 2)
3. Waits for both to complete

### `upload-images.sh` - Image Registry Upload

Uploads container image bundles (tar archives) to a container registry.

**Usage:**
```bash
./upload-images.sh BUNDLE_FILE [OPTIONS]

Options:
  --insecure  Allow insecure (HTTP) registry connections
  --jobs N    Number of parallel image uploads (default: 4)
```

**Examples:**
```bash
# Upload to registry with parallel jobs
REGISTRY_ENDPOINT=localhost:5000 \
  ./upload-images.sh artifacts/agent-images-bundle.tar --insecure --jobs 2

# Upload sleep app images
REGISTRY_ENDPOINT=registry.example.com \
  ./upload-images.sh artifacts/sleep-app-images-bundle.tar
```

**Environment Variables:**
- `REGISTRY_ENDPOINT` - Target registry address (required)

**How it works:**
1. Loads images from tar bundle into local podman storage
2. Tags images with registry prefix
3. Pushes images to registry in parallel
4. Used in CI/CD to populate E2E test registries

## Flavors

Flavors define OS base images and build configurations. Each flavor is a configuration file that contains both shell variables (for build script control) and build arguments (passed to podman build).

**How Flavor Files Work:**

The flavor configuration file is:
1. **Sourced by the build script** to read shell variables like `OS_ID` and `EXCLUDE_VARIANTS`
2. **Passed directly to podman build** via `--build-arg-file` flag, making all variables available as build arguments in the Containerfile

This means you can reference any flavor parameter in your Containerfile using `ARG` directives.

**Flavor Config Format** (`flavors/<name>.conf`):
```bash
# Shell variables (used by build script)
OS_ID=cs9-bootc
EXCLUDE_VARIANTS="v7"

# Build arguments (passed to Containerfile via --build-arg-file)
DEVICE_BASE_IMAGE=quay.io/centos-bootc/centos-bootc:stream9
ENABLE_CRB=0
EPEL_NEXT=1
```

### Flavor Parameters

| Parameter | Type | Description | Usage |
|-----------|------|-------------|-------|
| `OS_ID` | Shell variable | Unique identifier for the flavor/OS variant | Used in image tags (e.g., `base-cs9-bootc-latest`). Determines output image names. |
| `EXCLUDE_VARIANTS` | Shell variable | Space-separated list of variants to skip | Used by build script to filter which variants to build. Example: `"v7 v8"` |
| `DEVICE_BASE_IMAGE` | Build arg | Base bootc container image to start from | **Required**. The FROM image in base Containerfile. Example: `quay.io/centos-bootc/centos-bootc:stream9` |
| `ENABLE_CRB` | Build arg | Enable CodeReady Builder repository (0 or 1) | Set to `1` for CentOS Stream 10+ to access additional packages. CentOS Stream 9 uses `0`. |
| `EPEL_NEXT` | Build arg | Enable EPEL Next repository (0 or 1) | Set to `1` for CentOS Stream 9, `0` for CentOS Stream 10. Controls additional package availability. |
| `RPMS_FROM` | Build arg (optional) | Directory containing RPM packages | Defaults to `rpms` if not specified. Path to flightctl-agent and flightctl-selinux RPMs. |
| `TRUST_CA_FROM` | Build arg (optional) | Path to CA certificate file | If set, installs custom CA certificate to system trust store. Used for internal/test CAs. |
| `AGENT_CONFIG_FROM` | Build arg (optional) | Path to agent config.yaml file | If set, pre-installs agent configuration at `/etc/flightctl/config.yaml`. |
| `AGENT_CERTS_FROM` | Build arg (optional) | Directory containing agent certificates | If set, pre-installs agent certificates to `/etc/flightctl/certs/`. |

### Using Parameters in Containerfiles

In your Containerfile, declare the parameters you need:

```dockerfile
ARG DEVICE_BASE_IMAGE
ARG ENABLE_CRB=0
ARG EPEL_NEXT=1

FROM ${DEVICE_BASE_IMAGE}

RUN if [ "${ENABLE_CRB}" = "1" ]; then \
      dnf config-manager --set-enabled crb; \
    fi
```

All parameters from the flavor file are automatically available via `--build-arg-file`.

**Adding a New Flavor:**
1. Create `flavors/my-flavor.conf`
2. Set required parameters (`OS_ID`, `DEVICE_BASE_IMAGE`, etc.)
3. Set optional parameters as needed
4. Build: `FLAVORS=my-flavor ./build.sh --base`

## Variants

Variants are different device image configurations for testing.

**Available Variants:**
- `v2` - Dummy systemd services (test-e2e-dummy, test-e2e-crashing)
- `v3` - Another dummy service
- `v4` - Embedded compose app (sleep containers)
- `v5` - Compose app from common directory
- `v6` - SSH reload hook
- `v7` - MicroShift installation
- `v8` - Invalid compose app (for failure testing)
- `v9` - Custom system info collector
- `v10` - OpenTelemetry collector with metrics

**Variant Structure:**
```
variants/v{N}/
├── Containerfile          # Image definition
└── [variant-specific files]
```

**Creating a New Variant:**
1. Create `variants/vN/` directory
2. Add `Containerfile` with:
   ```dockerfile
   ARG BASE_IMAGE=quay.io/flightctl/flightctl-device:base
   FROM ${BASE_IMAGE}
   
   # Copy files from variant context
   COPY --from=variant-context myfile.txt /path/
   
   # Or from common
   COPY --from=common shared-file.yaml /path/
   ```
3. Add to `VARIANTS` list in build.sh or override: `VARIANTS="vN" ./build.sh --variants`

## Build Contexts

The build system uses named build contexts for clean file access:

### For Base Images:
- `project-root` - Project root directory (for go.mod, etc.)

### For Variants:
- `project-root` - Project root directory
- `variant-context` - Points to `variants/v{N}/` directory
- `common` - Points to `common/` directory

### For Apps:
- `common` - Points to `common/` directory

**Usage in Containerfiles:**
```dockerfile
# Copy from variant-specific directory
COPY --from=variant-context myfile.txt /dest/

# Copy from common shared directory
COPY --from=common shared.yaml /dest/

# Copy from project root
COPY --from=project-root go.mod /tmp/
```

## Applications

Application images use the naming pattern: `Containerfile.<app-name>.<version>`

**Example:**
```
apps/
├── Containerfile.sleep-app.v1
├── Containerfile.sleep-app.v2
└── Containerfile.my-app.v1
```

Auto-detected and built as:
- `quay.io/flightctl/sleep-app:v1-latest`
- `quay.io/flightctl/sleep-app:v2-latest`
- `quay.io/flightctl/my-app:v1-latest`

## Image Tagging

Built images receive multiple tags:

**Base Images:**
- `${IMAGE_REPO}:base-${OS_ID}-${TAG}` (canonical)
- `${IMAGE_REPO}:base`
- `${IMAGE_REPO}:base-${OS_ID}`
- `${IMAGE_REPO}:base-${TAG}`

**Variant Images:**
- `${IMAGE_REPO}:${variant}-${OS_ID}-${TAG}` (canonical)
- `${IMAGE_REPO}:${variant}`
- `${IMAGE_REPO}:${variant}-${OS_ID}`
- `${IMAGE_REPO}:${variant}-${TAG}`

**App Images:**
- `${APP_REPO}/${app-name}:${version}-${TAG}` (canonical)
- `${APP_REPO}/${app-name}:${version}`

## CI/CD Integration

In GitHub Actions, use with environment variables:

```yaml
- name: Build base images
  env:
    TAG: ${{ needs.compute-tag.outputs.tag }}
    SOURCE_GIT_TAG: ${{ needs.compute-tag.outputs.git_tag }}
    SOURCE_GIT_TREE_STATE: ${{ needs.compute-tag.outputs.git_tree_state }}
    SOURCE_GIT_COMMIT: ${{ needs.compute-tag.outputs.git_commit }}
    FLAVORS: cs9-bootc
  run: sudo -E ./test/scripts/agent-images/scripts/build.sh --base
```

## Common Workflows

### Build everything for testing:
```bash
./build.sh --base --variants --apps
```

### Build specific flavor and variants:
```bash
FLAVORS=cs9-bootc VARIANTS="v2 v3 v4" ./build.sh --base --variants
```

### Build with custom tag:
```bash
TAG=v1.2.3 ./build.sh --base --variants
```

### Build for CI with cache flags:
```bash
PODMAN_BUILD_EXTRA_FLAGS="--cache-from=quay.io/org/image:latest" ./build.sh --base
```

### Generate QCOW2 for single flavor:
```bash
OS_ID=cs9-bootc TAG=latest ./build_and_qcow2.sh
```

## Troubleshooting

**Problem:** Variants fail with "base image not found"
**Solution:** Build base first: `./build.sh --base`

**Problem:** Build is slow
**Solution:** Increase parallel jobs: `./build.sh --base --variants --jobs 8`

**Problem:** Need to change build arguments
**Solution:** Edit flavor conf file in `flavors/` directory

**Problem:** Want to skip certain variants
**Solution:** Set `EXCLUDE_VARIANTS` in flavor conf or override `VARIANTS` env var

## Best Practices

1. **Always build base before variants** - Variants depend on base images
2. **Use flavor configs** - Don't hardcode build args in scripts
3. **Use build contexts** - Reference files with `--from=variant-context` or `--from=common`
4. **Tag appropriately** - Use semantic versioning for production images
5. **Leverage parallelism** - Use `--jobs N` for faster builds
6. **Test locally first** - Build and test before pushing to CI

## Architecture Decisions

- **Named build contexts** - Cleaner than relative paths, explicit about file sources
- **Build arg files** - Easier to maintain than long command lines
- **Flavor configs** - Separate configuration from code
- **Auto-detection** - Apps and variants discovered automatically
- **Parallel builds** - xargs for variants, parallel jobs for build types
- **Multiple tags** - Support different access patterns (version, OS, latest)

