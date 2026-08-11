# Session Context

## Summary
Removed a stray `Requires: openssl` from the main (empty/never-built) RPM package scope in `packaging/rpm/flightctl.spec`. The correct dependency already exists in the `%package services` sub-package (added by EDM-4743).

## Key Design Decisions
The fix was already in place on `main` — the `%package services` sub-package has `Requires: openssl` at `flightctl.spec:89`. The only change was removing the dead `Requires: openssl` that was in the main package scope (which is never built), to avoid misleading future maintainers into thinking it provides openssl to sub-packages.

## Test Strategy
No unit tests — this is an RPM spec file change with no testable Go code path. Validation is at RPM build/install time. The lint suite was run and all 5 findings are pre-existing (verified against the base commit).

## Known Concerns
None. The change is minimal and only removes dead code.

## Artifacts
- `root-cause.md` — Root cause analysis
- `implementation-notes.md` — Detailed file changes and rationale
- `verification.md` — Test results and coverage
- `review.md` — Self-review findings
- `pr.md` — PR description
