# Implementation Notes: EDM-4985

## Files Modified

### internal/config/config.go
- **Lines 224-228**: Added `RhelBootcImageBuilder` field to `serviceImagesConfig` struct
  to hold RHEL-specific BIB image configuration.
- **Lines 233-234**: Added `defaultRhelBootcImageBuilderImage` constant set to
  `registry.redhat.io/rhel10/bootc-image-builder:latest`.
- **Lines 355-356**: Updated `NewDefaultImageBuilderWorkerConfig()` to include the
  RHEL BIB in the default service images.
- **Lines 397-408**: Added `EffectiveRhelBootcImageBuilderImage()` and
  `EffectiveRhelBootcImageBuilderSkipTLSVerify()` accessor methods following the
  existing nil-safe pattern.

### internal/imagebuilder_worker/tasks/imageexport.go
- **Lines 53-54**: Added `SourceImageName` field to `exportSource` struct to carry
  the original base image name from the ImageBuild.
- **Lines 463-466**: Added `isRHEL10Image()` helper that checks for "rhel10" in the
  source image name (case-insensitive).
- **Lines 468-492**: Updated `startBootcImageBuilderContainer()` signature to accept
  `exportSource` parameter. Added BIB selection logic: defaults to standard BIB,
  switches to RHEL BIB when `isRHEL10Image()` returns true.
- **Line 378**: Updated `executeExport()` call site to pass `exportSource`.
- **Lines 973**: In `validateAndNormalizeSource()`, added capture of
  `imageBuild.Spec.Source.ImageName` into the returned `exportSource`.

### deploy/helm/flightctl/templates/imagebuilder-worker/flightctl-imagebuilder-worker-config.yaml
- Added `rhelBootcImageBuilder` section mirroring `bootcImageBuilder` pattern.

### deploy/helm/flightctl/values.yaml
- Added `rhelBootcImageBuilder` configuration with documentation comments.

### deploy/podman/.../config.yaml.template
- Added conditional block for `rhelBootcImageBuilder` in the Go template.

### deploy/podman/service-config.yaml
- Added commented example for the new config option.

## Design Choices

**Why a separate config field instead of updating the default BIB?**
The centos-bootc BIB works correctly for RHEL 9, Fedora, and CentOS targets.
Changing the global default would risk regressions for those targets and would
require all users to have auth for `registry.redhat.io`. A separate RHEL-specific
BIB field isolates the change to RHEL 10+ exports only.

**Why string-based detection instead of OCI image inspection?**
Inspecting the OCI image config would require pulling/fetching image metadata,
adding network latency and complexity. The source image name from the ImageBuild
spec reliably contains the OS identifier (e.g., `rhel10/rhel-bootc`). A simple
case-insensitive check for "rhel10" covers all known RHEL 10 image naming
conventions.

**Why "rhel10" and not a regex for "rhel" + version >= 10?**
Simplicity. RHEL 10 is the first version with PQC keys. When RHEL 11 ships,
the detection function can be extended. Over-engineering the version parsing now
adds complexity without benefit.

## Test Strategy

### Covered
- `TestIsRHEL10Image`: 8 test cases covering RHEL 10 variants (paths, casing),
  non-RHEL images (centos, fedora), RHEL 9, and empty string.
- `TestEffectiveRhelBootcImageBuilderImage`: 4 cases covering nil config, custom
  override, empty override, and nil `RhelBootcImageBuilder` field.
- `TestNewDefaultImageBuilderWorkerConfig_IncludesRhelBIB`: Verifies the default
  config includes the RHEL BIB.

### Not covered (intentionally)
- Integration test for actual BIB container startup (requires Podman runtime and
  RHEL registry auth).
- End-to-end ImageExport with RHEL 10 source (requires full deployment with
  imagebuilder worker).
