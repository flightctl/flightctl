# RPM Spec Agent Guide

## Architecture

The upstream `flightctl.spec` is the source of truth. Downstream overrides behavior by redefining `%global` macros — no spec patches are needed. The spec's macro section is organized into four groups: build framework, FIPS configuration, package metadata, and downstream override hooks.

## When Overrides Are Required

- **Build invocation changes** — if modifying how binaries are compiled, linked, or what flags are passed, the new behavior likely needs to be an overridable macro. Downstream uses different compiler flags and hardening options.
- **Container image references** — any direct reference to a container image registry, name, or tag must be overridable. Downstream uses different registries and image naming conventions.
- **External network access** — anything that fetches from the internet (module proxies, `go install` from remote, etc.) must be overridable. Downstream builds in restricted network environments.

## When Overrides Are NOT Needed

- **File list changes** — adding or removing files from `%files` sections doesn't need overridability.
- **Scriptlet logic that applies universally** — `%pre`/`%post` logic that behaves the same regardless of where the RPM was built.
- **Version bumps and changelog updates** — these are mechanical changes, not behavioral.
- **Build dependencies** — `BuildRequires` additions don't need override hooks.

## Makefile Integration

The Makefile handles version injection and FIPS auto-detection. Do not duplicate this logic in the spec. The Makefile accepts flags from the command line and environment, which is how downstream passes additional compiler and linker flags without modifying the build invocation.
