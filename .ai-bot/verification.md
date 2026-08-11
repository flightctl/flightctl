# Verification Report: EDM-2740

## Test Results
- **Package**: `internal/service/common`
- **Result**: PASS (all tests)
- **New tests**: 10 test cases in `TestUpdateServerSideLifecycleStatus`

## Test Cases
| # | Scenario | Expected | Result |
|---|----------|----------|--------|
| 1 | No decommissioning condition | No change | PASS |
| 2 | Enrolled → DecomStarted | Decommissioning | PASS |
| 3 | Decommissioning → DecomComplete | Decommissioned | PASS |
| 4 | Decommissioning → DecomError | Decommissioned | PASS |
| 5 | **Decommissioned → DecomStarted** | **Blocked** (regression) | PASS |
| 6 | Decommissioned → DecomComplete | Blocked (terminal) | PASS |
| 7 | Decommissioned → DecomError | Blocked (terminal) | PASS |
| 8 | Decommissioning → DecomStarted (re-send) | No change | PASS |
| 9 | Unknown → DecomStarted | Decommissioning | PASS |
| 10 | Enrolled → DecomComplete (skip) | Decommissioned | PASS |

## Lint
- `golangci-lint run ./internal/service/common/...` — 0 issues

## Build
- `go build ./internal/service/common/...` — success
