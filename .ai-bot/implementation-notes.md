# Implementation Notes — EDM-4984

## Files Modified

- `packaging/rpm/flightctl.spec:30` — Removed stray `Requires: openssl` from the main (empty) package scope. This dependency was never effective because the main package has no `%files` section and is never built. The correct `Requires: openssl` already exists in the `%package services` sub-package (now at line 89 after removal).

## Design Choices

- **Chose removal over relocation**: The `Requires: openssl` at the main package level was dead code. Since `%package services` already declares `Requires: openssl` (added in EDM-4743), the fix is simply to remove the stray line.
- **No new dependencies added**: The correct dependency was already in place from the encryption-at-rest feature (EDM-4743). This change only removes the misleading duplicate on the empty main package.

## Test Strategy

- **No unit tests added**: This is an RPM spec file change (infrastructure/configuration). There is no testable code path — the dependency is validated at RPM build/install time, not in Go code.
- **Verification**: Confirmed via `grep` that `Requires: openssl` appears exactly once, in the `%package services` sub-package.

## Alternatives Considered

- **Leave the stray line**: Considered leaving the dead `Requires: openssl` on the main package. Rejected because it's misleading — a maintainer might think it provides the openssl dependency to sub-packages, when RPM sub-packages require their own explicit dependencies.
