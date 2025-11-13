# Agent Images V2 Implementation Summary (Flavorized)

## What Was Created

### 1. Directory Structure
```
test/scripts/agent-images-v2/
├── base/Containerfile
├── flavors/{cs9.env, cs10.env}
├── variants/v{2..10}/Containerfile
├── common/files/{prepare_otel_config.sh, otelcol.service}
└── scripts/{build.sh, bundle.sh, qcow2.sh}
```

### 2. Containerfiles
- `base/Containerfile` parameterized with `ARG BOOTC_BASE_IMAGE` and multi-stage (`prebase`, `base`, `base-external`).
- Variants keep `ARG BASE_IMAGE` and only layer variant-specific content.

### 3. Build + Bundle + QCOW2
- `scripts/build.sh` builds all or selected flavors (`FLAVORS=cs10`) and variants.
- `scripts/bundle.sh` creates per-flavor bundles with four tags per image:
  - `name`, `name-{os}`, `name-{tag}`, `name-{os}-{tag}`
  - Plain `name` exists only inside bundles.
- `scripts/qcow2.sh` emits qcow2 from `base-{os}-{tag}` to `artifacts/agent-qcow2-{os}/`.

### 4. GitHub Actions Workflow Changes
- `build-agent-images-unified` now uses a matrix over `os_id: [cs9, cs10]`:
  1) Build images for the flavor
  2) Bundle images → `agent-images-bundle-{os}.tar`
  3) Build qcow2 → `agent-qcow2-image-{os}/disk.qcow2`

## Design

### Artifact Reuse
RPMs built in `build-rpms` are reused by image builds.

### Certificates
Not embedded; produced at runtime via the agent and OTEL prep script.

### Tagging
Canonical build tags are `*-{os}-{tag}`. Bundles include additional tags; registries only receive suffixed tags on release.

### CI
Matrix isolates flavors, produces separate bundles and qcow2 per flavor.

## Local Usage
```bash
TAG=test ./test/scripts/agent-images-v2/scripts/build.sh
TAG=test OS_ID=cs10 ./test/scripts/agent-images-v2/scripts/bundle.sh
TAG=test OS_ID=cs10 ./test/scripts/agent-images-v2/scripts/qcow2.sh
```

## Notes
- User credentials: `user:user` with sudo access
- Systemd services enabled: flightctl-agent, firewalld, podman
- Caching directories speed up qcow2 builds

