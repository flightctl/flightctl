# Verification — EDM-4984

## Test Gate Check

No test files modified — this is an RPM spec file change (infrastructure/configuration) with no testable code path. The openssl dependency is validated at RPM build/install time, not in Go test suites.

## Verification Performed

1. **Grep check**: `Requires: openssl` appears exactly once in the spec file, at line 89, inside the `%package services` sub-package — confirmed correct.
2. **Diff review**: The change removes exactly 2 lines (the stray `Requires: openssl` and its trailing blank line) from the main package scope. No other changes.
3. **Spec file structure**: The main package remains empty (no `%files` section). All sub-packages retain their existing dependencies.

## Result

PASS — The correct dependency is in place; the dead code has been removed.
