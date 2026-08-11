# Implementation Notes: EDM-2740

## Files Modified

### `internal/service/common/device.go:164-196` — `updateServerSideLifecycleStatus()`
- **Terminal state guard (line 168-170)**: Added early return when current lifecycle status is `Decommissioned`. This is the primary fix — it prevents any transition out of the terminal state, regardless of what the agent reports.
- **`if/else if` chain (lines 180-194)**: Changed three independent `if` statements to a single `if/else if` chain. This makes the conditions mutually exclusive and prevents the last-wins behavior of the original code.
- **Idempotent `DecomStarted` (line 188-193)**: Added guard so that if the device is already `Decommissioning` and the agent re-sends `DecomStarted`, no state change occurs. This prevents spurious change notifications.
- **Return expression fix (line 196)**: Changed `&&` to `||` in the return statement. The original `device.Status.Lifecycle.Status != lastLifecycleStatus && device.Status.Lifecycle.Info != lastLifecycleInfo` required BOTH status and info to differ, which would incorrectly return `false` when only one changed. Also used `lo.FromPtr()` for safe nil-pointer comparison of the info strings.

### `internal/service/common/device_test.go:551-723` — `TestUpdateServerSideLifecycleStatus`
- Added 10 table-driven test cases covering all lifecycle state transitions and the specific regression scenario described in the issue.

## Design Choices
- **Early return pattern** over state machine map: The lifecycle has only 4 states and a small number of valid transitions. An early return for the terminal state plus if/else if is simpler and more readable than a full state transition table.
- **`Decommissioned` as sole terminal state**: Only `Decommissioned` is treated as terminal. `Decommissioning` allows forward transition to `Decommissioned` but blocks re-application of `DecomStarted`.
- **Allow skip transitions**: `Enrolled` → `Decommissioned` (via `DecomComplete`) is allowed. This handles edge cases where the `DecomStarted` condition was never observed by the server.

## Test Strategy
- **Covered**: All valid forward transitions, all invalid backward transitions (the bug), idempotent re-sends, and edge cases (Unknown state, skip transitions).
- **Intentionally excluded**: Integration/e2e tests with real agent communication — the unit tests fully cover the state machine logic in isolation.
