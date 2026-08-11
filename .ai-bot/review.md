# Self-Review: EDM-2740

## Review Summary
The fix is minimal, focused, and correctly addresses the reported issue.

## Changes Reviewed

### `internal/service/common/device.go` — `updateServerSideLifecycleStatus()`
1. **Terminal state guard**: Early return when current status is `Decommissioned` — prevents any backward transition. This is the core fix.
2. **`if/else if` chain**: Changed three independent `if` statements to `if/else if`, making the conditions mutually exclusive (only one branch executes).
3. **Idempotent `DecomStarted`**: Added inner guard so re-sending `DecomStarted` when already `Decommissioning` is a no-op (prevents spurious "changed" signals).
4. **Return expression fix**: Changed `&&` to `||` — the original required both status and info to change to report a change, which incorrectly returned `false` when only the info text differed.

### `internal/service/common/device_test.go` — `TestUpdateServerSideLifecycleStatus`
10 table-driven test cases covering:
- No condition present (no change)
- Forward transitions: Enrolled→Decommissioning, Decommissioning→Decommissioned (complete + error)
- **Regression tests**: Decommissioned→Decommissioning blocked (the exact bug from the issue)
- Decommissioned remains terminal for all condition types
- Idempotent re-send of DecomStarted
- Unknown→Decommissioning (edge case)
- Enrolled→Decommissioned (skip Decommissioning; allowed because DecomComplete is valid)

## Findings
- No CRITICAL or HIGH severity issues found.
- The fix correctly models the lifecycle state machine described in the issue.
