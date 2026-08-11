## Summary
- Add lifecycle status transition guardrails to `updateServerSideLifecycleStatus()` to enforce a well-defined state machine: `Enrolled` → `Decommissioning` → `Decommissioned` (terminal)
- Prevent backward transitions — a device in `Decommissioned` state can no longer be moved back to `Decommissioning` by agent-reported conditions
- Fix return expression bug that used `&&` instead of `||` for change detection
- Add 10 table-driven regression tests covering all valid and invalid lifecycle transitions

## Test plan
- [x] All 10 new lifecycle status test cases pass (`TestUpdateServerSideLifecycleStatus`)
- [x] All existing tests in `internal/service/common` pass
- [x] Lint passes cleanly (`golangci-lint run ./internal/service/common/...`)
- [ ] Verify no regression in integration tests (`make integration-test`)
- [ ] Verify decommissioning flow works end-to-end in a deployed environment

🤖 Generated with [Claude Code](https://claude.com/claude-code)
