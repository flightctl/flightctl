# Root Cause Analysis: EDM-4985

## Summary

ImageExport fails for RHEL 10 bootc images because the default
bootc-image-builder (BIB) container image is based on Fedora 38, whose
crypto libraries cannot process Red Hat's new PQC (Post-Quantum
Cryptography) ML-DSA GPG release key (certificate FCD355B305707A62).

## Root Cause

The `rpmkeys --import` command inside the centos-bootc BIB container rejects
the PQC key with "Policy rejects ... No binding signature at time ..." because
the Fedora 38 GPG stack lacks ML-DSA algorithm support.

**Affected code path:**

1. `internal/config/config.go:232` — hardcoded default BIB image
   (`quay.io/centos-bootc/bootc-image-builder@sha256:...`)
2. `internal/config/config.go:382-388` — `EffectiveBootcImageBuilderImage()`
   returns either user override or default; no OS-aware selection
3. `internal/imagebuilder_worker/tasks/imageexport.go:478` — retrieves the
   single global BIB image for all exports regardless of source OS
4. `internal/imagebuilder_worker/tasks/imageexport.go:462-559` —
   `startBootcImageBuilderContainer()` starts the BIB with no awareness of
   the source image's OS

## Fix Strategy

Add a separate `rhelBootcImageBuilder` service image config that defaults to
`registry.redhat.io/rhel10/bootc-image-builder:latest`. Detect when the
source image is RHEL 10+ by checking the ImageBuild source image name for
"rhel10" and select the RHEL-specific BIB automatically.

Files to modify:
- `internal/config/config.go` — add RHEL BIB config + accessors
- `internal/imagebuilder_worker/tasks/imageexport.go` — thread source image
  info, add detection, select BIB
- Helm chart — expose new config option
- Unit tests for detection logic and config

## Confidence: 95%
