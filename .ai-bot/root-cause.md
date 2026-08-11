# Diagnosis — EDM-4984: flightctl-certs-init.service fails with "openssl: command not found"

## Root Cause

The `Requires: openssl` dependency was originally placed in the main RPM package scope (`flightctl.spec:30`), which is declared as empty and never built. RPM sub-packages do not inherit `Requires` from the main package, so the `flightctl-services` sub-package shipped without an openssl dependency.

This was corrected in commit `45b8dcd1` (EDM-4743: Wire encryption-at-rest infrastructure) which added `Requires: openssl` to the `%package services` sub-package (`flightctl.spec:91`). However, the stray `Requires: openssl` on the main (empty) package at line 30 was not removed.

## Evidence

- `packaging/rpm/flightctl.spec:30` — `Requires: openssl` in main package scope (dead code, main package has no `%files` section)
- `packaging/rpm/flightctl.spec:91` — `Requires: openssl` in `%package services` (correct placement, added in EDM-4743)
- `packaging/rpm/flightctl.spec:36` — Comment: "# Main package is empty and not created"
- `deploy/helm/flightctl/scripts/generate-certificates.sh` — uses `openssl` extensively for certificate generation
- `deploy/scripts/init_certs.sh` — calls `generate-certificates.sh`
- Both scripts are listed in `%files services` (lines 689, 692)

## Timeline

- `fbb99a10` (EDM-885: RPM versioning) — `Requires: openssl` added to main package scope (ineffective)
- `45b8dcd1` (EDM-4743: Wire encryption-at-rest infrastructure) — `Requires: openssl` added to `%package services` (correct fix)

## Affected Components

- `packaging/rpm/flightctl.spec:30` — stray dependency on empty main package (to be removed)
- `packaging/rpm/flightctl.spec:91` — correct dependency already in place

## Impact Assessment

- Severity: Critical (on version 1.2 where the services sub-package fix was absent)
- User impact: Fresh installs on minimal RHEL systems cannot initialize certificates
- Blast radius: Only the `flightctl-services` package; no other sub-packages use openssl directly

## Hypotheses Tested

1. **Missing `Requires: openssl` in services sub-package** (HIGH confidence) — Confirmed. The dependency was on the wrong RPM scope. Fixed in 1.3 by EDM-4743.
2. **Other sub-packages may need openssl** — Checked. Only `flightctl-services` ships the certificate generation scripts.

## Recommended Fix Approach

Remove the stray `Requires: openssl` from the main (empty) package scope at line 30. The correct dependency at line 91 (`%package services`) is already in place. This is a cleanup to remove dead code that could mislead future maintainers.

## Confidence: High (99%)
