# E2E wall-time work (`e2e-under-30m`)

Goal: full PR E2E workflow (build + cs9 sanity) **≤ 30 minutes** wall.

Baselines (measured):

| Tip | Run | Wall | Max cs9 job | Notes |
|-----|-----|------|-------------|--------|
| `e2e-container-device` (pre-restack) | `29908219937` | ~53m | ~36.6m | greenboot ~12.8m |
| `e2e-slow-spec-fixes` | `30201662019` | ~51m | ~35.6m | greenboot ~6m; helm degraded ~8.4m; backup_restore ~12m |

Stack: `main` ← `#3221` ← `#3277` ← **`e2e-under-30m`** (this branch). Prefer **optimize** over dropping coverage; concessions below are documented deliberately.

## Concessions (coverage kept, shape changed)

### Prefer optimize — still on `sanity`

1. **`89141` backup/restore** — RV divergence via **config-only** fleet motd updates (not bootc OS v2→v3→v4). Primary device is **`needdevice`** (container). Still asserts: 3 ERs, backup/restore, ConflictPaused, OutOfDate, resume → Online+UpToDate. OS rollout remains in agent_update + non-sanity `89194`.
2. **`78753` hooks** — **one** OS reboot (V6 + lifecycle hooks together). Lifecycle log asserts run after that reboot; sshd/embedded-hook checks stay config-only afterward. Dropped second OS→Base cycle.
3. **`77671` embedded app** — same two OS cycles (v4 then base); waits collapsed to `WaitForDeviceNewRenderedVersionWithReboot`. SELinux checked after v4.
4. **`87531` helm degraded** — sanity keeps Healthy→Degraded; Error-state path is a separate **slow** It (same checkpoint id). `delayBeforeFail` 20→5.
5. **`88004` helm auth** — sanity keeps helm+registry auth; rootful/rootless quadlet auth follow-ups are a separate **slow** It.
6. **`87279` greenboot** — sanity keeps rollback + no-retry; recovery OS→good image is a separate **slow** It. Stability 45s→20s; e2e health override 10/10/2→5/5/1.

### LPT packing (not deleted)

7. **`88425` journal persistent** — removed `sanity` label only (still runs; another full rollback cycle). Prefer re-adding to sanity once greenboot path is stably ≤5m and LPT allows.

### CI prep (not a test concession)

8. Agent qcow/bundle download runs **in parallel** with kind+Helm deploy (`run-e2e-tests.yaml`, detached `gh run download`).
9. Helm `BeforeSuite` uses `SetupWorkerHarnessWithoutVM` (VM still per-spec in BeforeEach).

## Still needed for ≤30m (not done)

- Prebaked kind node / further prep cut (target job prep ~5m).
- Build critical path stay ≤~10m on stacked tip.
- Re-measure LPT on this tip after CI; chase next max (likely restore duration, hooks/v4 residual, helm BeforeSuite aux upload).

## Non-goals

- `GINKGO_PROCS` as the primary strategy.
- Deleting backup/restore or greenboot sanity coverage.
