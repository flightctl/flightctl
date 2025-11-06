# Agent Images V2 Implementation Summary

## What Was Created

### 1. Directory Structure
```
test/scripts/agent-images-v2/
├── Containerfile-base       # Base agent image
├── Containerfile-v2         # Variant with test services
├── Containerfile-v3         # Variant with another service
├── Containerfile-v4         # Variant with compose app
├── build_agent_images.sh    # Build script
├── README.md                # Documentation
└── IMPLEMENTATION.md        # This file
```

### 2. Containerfiles

All containerfiles have been modified to:
- Use `quay.io/flightctl/flightctl-device` as image repository
- Accept RPMs from `rpms/` directory (downloaded artifacts)
- **NOT include agent certificates** (will be injected at runtime)
- Accept build args: `SOURCE_GIT_TAG`, `SOURCE_GIT_TREE_STATE`, `SOURCE_GIT_COMMIT`
- Use `ARG BASE_IMAGE` for variant images to support flexible tagging

### 3. Build Script

`build_agent_images.sh`:
- Downloads RPMs from artifacts (via parent workflow)
- Builds base image first, then variants in parallel
- Optionally builds qcow2 image using bootc-image-builder
- Tags images with format: `quay.io/flightctl/flightctl-device:<variant>-<tag>`
- Environment variables for customization

### 4. GitHub Actions Workflow Changes

Added two new jobs to `.github/workflows/pr-build-artifacts.yaml`:

#### Job: `build-agent-images`
- **Depends on:** `compute-tag`, `build-rpms`
- **Steps:**
  1. Checkout code
  2. Prepare version variables
  3. Download RPM artifacts from `build-rpms` job
  4. Build all agent container images (base, v2, v3, v4)
  5. Create bundle of all images
  6. Upload bundle as artifact
- **Artifact:** `agent-images-bundle.tar` (compression: 1, retention: 1 day)

#### Job: `build-agent-qcow2`
- **Depends on:** `compute-tag`, `build-agent-images`
- **Steps:**
  1. Checkout code
  2. Download agent images bundle
  3. Load images into podman
  4. Build qcow2 from base image using bootc-image-builder
  5. Upload qcow2 as artifact
- **Artifact:** `agent-qcow2-image/disk.qcow2` (compression: 1, retention: 1 day)

#### Updated Job: `artifacts-ready`
- Added dependencies: `build-agent-images`, `build-agent-qcow2`
- Updated PR comment to include agent artifacts section

## Key Design Decisions

### 1. Artifact Reuse
RPMs built once in `build-rpms` job are downloaded and reused in `build-agent-images` job. This:
- Ensures consistency across builds
- Reduces build time (no duplicate RPM builds)
- Follows CI/CD best practices

### 2. Certificate Handling
Certificates are **NOT** embedded in images because:
- Allows images to be reusable across different environments
- More secure (no hardcoded credentials)
- Follows cloud-native practices
- Configuration can be injected at runtime via cloud-init or volume mounts

### 3. Image Tagging
Images tagged with commit-based version (e.g., `base-v0.6.0-abc1234`) to:
- Track which code version produced the image
- Enable reproducible builds
- Support parallel development (multiple PRs)

### 4. Separate QCOW2 Job
QCOW2 build is in separate job because:
- It's resource-intensive and time-consuming
- Allows parallel execution with other jobs
- Can fail independently without affecting image builds
- Reuses the agent images bundle (no duplicate image builds)

### 5. Compression and Retention
- Compression level 1: Fast upload/download, good balance
- Retention 1 day: Sufficient for PR testing, saves storage

## Usage in Workflows

### Download artifacts in subsequent jobs:
```yaml
- name: Download agent images
  uses: actions/download-artifact@v4
  with:
    name: agent-images-bundle

- name: Load images
  run: podman load -i agent-images-bundle.tar
```

### Download artifacts locally:
```bash
gh run download <run-id> -n "agent-images-bundle" -n "agent-qcow2-image"
```

## Testing Workflow

1. **PR opened** → Workflow triggered
2. **build-rpms** → Creates RPMs
3. **build-agent-images** → Downloads RPMs, builds images, creates bundle
4. **build-agent-qcow2** → Downloads bundle, creates qcow2
5. **artifacts-ready** → Posts PR comment with download instructions
6. **E2E tests** (future) → Download artifacts and run tests

## Differences from Original Implementation

| Aspect | Original | V2 |
|--------|----------|-----|
| RPM Source | Local build (`bin/rpm/`) | Downloaded from artifacts |
| Registry | `localhost:5000` | `quay.io/flightctl/flightctl-device` |
| Certificates | Embedded in image | Not included (runtime injection) |
| Image Tags | `localhost:5000/...:base` | `quay.io/.../...:base-v0.6.0` |
| Target | Local dev/test | CI/CD artifact reuse |
| Configuration | Included in image | Injected at runtime |

## Future Enhancements

1. **Push to Quay.io**: Add job to push images to registry (for releases)
2. **E2E Integration**: Update e2e tests to use these artifacts
3. **Cache Strategy**: Add caching for bootc-image-builder layers
4. **Multi-arch**: Build arm64 images for multi-arch support
5. **Signing**: Add image signing for security

## Validation

To validate the implementation:

1. Create a PR
2. Wait for workflow to complete
3. Check PR comment for artifact links
4. Download artifacts:
   ```bash
   gh run download <run-id> -n "agent-images-bundle"
   podman load -i agent-images-bundle.tar
   podman images | grep flightctl-device
   ```
5. Verify images are tagged correctly
6. Test qcow2 image boots successfully

## Notes

- All images use CentOS Stream 9 bootc as base
- User credentials: `user:user` with sudo access
- Systemd services enabled: flightctl-agent, firewalld, podman
- Custom info collectors included for testing

