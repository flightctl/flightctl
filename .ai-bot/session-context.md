# Session Context

## Summary
Added RHEL-specific bootc-image-builder (BIB) support to fix ImageExport failures
on RHEL 10 bootc images. The default centos-bootc BIB (Fedora 38 based) cannot
handle RHEL 10's PQC GPG signing keys; the fix auto-detects RHEL 10 source images
and uses a RHEL-specific BIB (`registry.redhat.io/rhel10/bootc-image-builder:latest`).

## Key Design Decisions
- Added a separate `rhelBootcImageBuilder` service image config rather than
  changing the global default BIB, to avoid regressions on RHEL 9/Fedora targets.
  Core change in `internal/config/config.go:224-228` (struct) and `:397-408` (accessors).
- Detection uses a simple case-insensitive "rhel10" substring check on the
  ImageBuild source image name (`internal/imagebuilder_worker/tasks/imageexport.go:463-466`).
  This is sufficient for known RHEL 10 naming conventions.
- Threaded source image info through the export flow by adding `SourceImageName`
  to the `exportSource` struct (`imageexport.go:53-54`) and populating it from
  `imageBuild.Spec.Source.ImageName` in `validateAndNormalizeSource` (`:973`).
- BIB selection in `startBootcImageBuilderContainer` (`:486-492`): defaults to
  standard BIB, overrides to RHEL BIB when RHEL 10 source is detected.

## Test Strategy
- `isRHEL10Image()`: 8 table-driven test cases covering RHEL 10 variants,
  non-RHEL images, case sensitivity, and empty input.
- `EffectiveRhelBootcImageBuilderImage()`: 4 cases for nil/empty/override configs.
- Default config factory test verifying RHEL BIB inclusion.
- No integration/e2e tests — would require Podman runtime and RHEL registry auth.

## Known Concerns
- Self-review was clean. No unresolved issues.
- The RHEL BIB default (`registry.redhat.io/rhel10/bootc-image-builder:latest`)
  requires RHEL registry authentication via `pullSecretName`. Users building
  RHEL 10 images should already have this auth available.
- Future RHEL versions (11+) will need the detection function updated.

## Artifacts
- `root-cause.md` — Root cause analysis
- `implementation-notes.md` — Detailed file changes and rationale
- `verification.md` — Test results and coverage
- `review.md` — Self-review findings
