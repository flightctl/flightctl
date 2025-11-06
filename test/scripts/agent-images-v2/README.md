# Agent Images V2 - CI/CD Optimized Build

This directory contains containerfiles and build scripts for creating agent images optimized for CI/CD pipelines.

## Key Differences from Original agent-images

### Original approach (`agent-images/`)
- Uses local RPMs from `bin/rpm/`
- Uses local container registry at `localhost:5000`
- Includes agent configuration and certificates in images
- Designed for local development and testing

### New approach (`agent-images-v2/`)
- Downloads RPMs from GitHub Actions artifacts
- Tags images with `quay.io/flightctl/flightctl-device`
- **Does not include agent certificates** - configuration injected at runtime
- Designed for CI/CD artifact reuse
- Images tagged with commit-based version (e.g., `base-v0.6.0-abc1234`)

## Images Built

| Image | Description | Base |
|-------|-------------|------|
| `base` | Base bootc image with flightctl-agent installed | centos-bootc:stream9 |
| `v2` | Base + dummy systemd services (test-e2e-dummy, test-e2e-crashing) | base |
| `v3` | Base + another dummy systemd service | base |
| `v4` | Base + embedded podman-compose application | base |

## Build Process

### In GitHub Actions

The build is triggered automatically in `.github/workflows/pr-build-artifacts.yaml`:

1. **build-rpms** job creates RPM artifacts
2. **build-agent-images** job:
   - Downloads RPM artifacts
   - Builds all agent container images in parallel
   - Creates bundle of all images
3. **build-agent-qcow2** job:
   - Downloads agent images bundle
   - Converts base image to qcow2 using bootc-image-builder

### Artifacts Produced

- `agent-images-bundle.tar` - All agent images in docker-archive format
- `agent-qcow2-image/disk.qcow2` - Bootable qcow2 disk image

Both artifacts:
- Use compression level 1 (faster upload/download)
- Retained for 1 day
- Available for e2e tests

## Usage

### Download from GitHub Actions

```bash
gh run download <run-id> -n "agent-images-bundle" -n "agent-qcow2-image"
```

### Load images locally

```bash
podman load -i agent-images-bundle.tar
```

### Use in e2e tests

The qcow2 image can be used directly for VM-based e2e tests:

```bash
qemu-img create -f qcow2 -b agent-qcow2-image/disk.qcow2 -F qcow2 test-vm.qcow2
```

## Local Development

To build locally:

```bash
# Build RPMs first
make rpm

# Build agent images
TAG=test ./test/scripts/agent-images-v2/build_agent_images.sh
```

Environment variables:
- `TAG` - Image tag (default: latest)
- `IMAGE_REPO` - Base image repository (default: quay.io/flightctl/flightctl-device)
- `PARALLEL_JOBS` - Number of parallel builds (default: 4)
- `BUILD_QCOW2` - Build qcow2 image (default: true)

## Image Configuration

Since certificates are not embedded in images, they must be provided at runtime:

### For containers
Mount configuration and certificates:
```bash
podman run -v /path/to/config.yaml:/etc/flightctl/config.yaml \
           -v /path/to/certs:/etc/flightctl/certs \
           quay.io/flightctl/flightctl-device:base-v0.6.0
```

### For VMs
Use cloud-init or similar to inject configuration during provisioning.

## Containerfile Structure

### Containerfile-base
- Installs flightctl-agent and flightctl-selinux RPMs
- Sets up user/password (user:user)
- Installs dependencies (greenboot, podman-compose, firewalld)
- Creates directory structure for runtime configuration
- Adds custom system info collectors

### Containerfile-v2, v3, v4
- Build from base image using `ARG BASE_IMAGE`
- Add variant-specific services or applications
- Minimal layers for efficiency

## Notes

- All images use CentOS Stream 9 bootc as base
- Images are tagged with version for artifact tracking
- QCOW2 images are created using the official bootc-image-builder
- Caching directories are used to speed up qcow2 builds

