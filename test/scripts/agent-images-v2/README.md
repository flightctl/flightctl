# Agent Images V2 - Flavorized (cs9, cs10)

This directory builds agent images for CentOS Stream 9 and 10 using a flavor-aware layout, optimized for CI/CD. Certificates are injected at runtime; not embedded.

## Layout
- `base/Containerfile`: Parametric base image with `ARG BOOTC_BASE_IMAGE`
- `flavors/`: Flavor envs
  - `cs9.env`, `cs10.env`
- `variants/vN/Containerfile`: Variant layers (v2..v10)
- `common/files/`: Shared files (otel unit and prep script)
- `scripts/`: Build tooling
  - `build.sh`, `bundle.sh`, `qcow2.sh`

## Tagging
For each flavor `os` in {cs9, cs10} and build `tag`:
- Base: `base`, `base-{os}`, `base-{tag}`, `base-{os}-{tag}`
- Variant `vN`: `vN`, `vN-{os}`, `vN-{tag}`, `vN-{os}-{tag}`
Only suffixed tags are pushed to registries; plain tags exist only inside bundles.

## CI
Workflow `.github/workflows/pr-build-artifacts.yaml` runs a matrix over `os_id: [cs9, cs10]` and produces:
- `agent-images-bundle-{os}.tar` (docker-archive)
- `agent-qcow2-image-{os}/disk.qcow2`

## Local build
```bash
# Build RPMs first
make rpm

# Build both flavors
TAG=test ./test/scripts/agent-images-v2/scripts/build.sh

# Build a single flavor
TAG=test FLAVORS=cs10 ./test/scripts/agent-images-v2/scripts/build.sh

# Create a bundle for a flavor
TAG=test OS_ID=cs10 ./test/scripts/agent-images-v2/scripts/bundle.sh

# Build qcow2 for a flavor
TAG=test OS_ID=cs9 ./test/scripts/agent-images-v2/scripts/qcow2.sh
```

## Variants
- v2: dummy and crashing services
- v3: another dummy service
- v4/v5: embedded compose apps
- v6: sshd hook
- v7: microshift
- v8: invalid compose app
- v9: misc test content
- v10: otelcol with system metrics

## Notes
- Uses bootc-image-builder for qcow2
- Build context is repo root to resolve paths consistently

