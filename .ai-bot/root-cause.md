# Root Cause Analysis: EDM-2740

## Summary
The `updateServerSideLifecycleStatus` function in `internal/service/common/device.go:164-195` lacks guardrails to enforce valid lifecycle state transitions. It blindly sets lifecycle status based on agent-reported conditions without checking the current lifecycle status.

## Root Cause
1. **No state machine enforcement**: The function uses three independent `if` statements that evaluate the decommissioning condition. The last matching condition wins, and there is no check against the device's current lifecycle status.
2. **Terminal state not protected**: `Decommissioned` should be a terminal state, but the function would move a device from `Decommissioned` back to `Decommissioning` if the agent sends a `DecomStarted` condition.
3. **No forward-only transitions**: The valid lifecycle state machine is: `Enrolled` → `Decommissioning` → `Decommissioned`. The current code doesn't enforce this ordering.

## Affected Code
- `internal/service/common/device.go:164-195` — `updateServerSideLifecycleStatus()`

## Fix Strategy
Add a guard at the top of the function: if the device is already in `Decommissioned` state, return immediately (no transitions allowed out of a terminal state). Also restructure the condition checks to use `if/else if` to make the logic mutually exclusive and clearer. For the `Decommissioning` state, only allow forward transition to `Decommissioned`.
