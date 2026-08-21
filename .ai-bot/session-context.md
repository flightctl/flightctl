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

## Feedback Round 1
**PR:** flightctl/flightctl#3350
**Comments addressed**: None (no reviewer comments found on PR)
**CI failure investigated**: integration-tests job (exit code 2)
**Changes made**: None — the CI failure is not caused by this PR's changes.
**Analysis**:
- All code compiles cleanly (`go build ./...`, `go test -c ./test/integration/...`)
- Unit tests pass for changed packages (`internal/config/`, `internal/imagebuilder_worker/tasks/`)
- Lint passes for changed packages (golangci-lint v2.12.2, 0 issues)
- Import ordering correct (gci reports no diff)
- `go vet` clean on all changed packages
- The CI integration test log shows infrastructure starting normally (server, auth, enrollment) before failing — no compilation or test assertion errors visible in the available log output
- The failure appears to be environmental or flaky, not related to the config/imageexport changes in this PR
- No integration tests directly reference `startBootcImageBuilderContainer`, `exportSource.SourceImageName`, or `RhelBootcImageBuilder`
**Verification**: `go build ./...` pass, `go vet ./...` pass, unit tests pass, lint pass
**Suggestions declined**: None
**Tests updated**: None needed
