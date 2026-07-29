# E2E wall-time work (`e2e-under-30m`)

Goal: full PR E2E workflow (build + cs9 sanity) **≤ 30 minutes** wall.

Baselines (measured):

| Tip | Run | Wall | Max cs9 job | Notes |
|-----|-----|------|-------------|--------|
| `e2e-container-device` (pre-restack) | `29908219937` | ~53m | ~36.6m | greenboot ~12.8m |
| `e2e-slow-spec-fixes` | `30201662019` | ~51m | ~35.6m | greenboot ~6m; helm degraded ~8.4m; backup_restore ~12m |
| `e2e-under-30m` (pre this cut) | `30209449889` | ~45m | ~29m | build ~15m; LPT agent suite ~17.5m (77671 ~6.6m) |
| `e2e-under-30m` (LPT dir-narrow + spec trim baseline) | `30244616779` | ~42.4m | ~26.9m (cs10 node4) | build ~15.4m; per-shard ginkgo compile/launch tax ~4.4m (see below) |
| `e2e-under-30m` (dir-narrow + `78684` split) | `30484730568` | ~41-43m | ~26.9m (cs10 node4) | build ~14.9m (compute-tag 10s + build-rpms ~5m49s + build-agent-images ~8m25s); no net wall improvement (see "LPT dir-narrow + spec trim" below) |

Stack: `main` ← `#3221` ← `#3277` ← **`e2e-under-30m`** (this branch). Prefer **optimize** over dropping coverage; concessions below are documented deliberately.

## Concessions (coverage kept, shape changed)

### Prefer optimize — still on `sanity`

1. **`89141` backup/restore** — RV divergence via **config-only** fleet motd updates (not bootc OS v2→v3→v4). Primary device is **`needdevice`** (container). Still asserts: 3 ERs, backup/restore, ConflictPaused, OutOfDate, resume → Online+UpToDate. OS rollout remains in agent_update + non-sanity `89194`.
2. **`78753` hooks** — **one** OS reboot (V6 + lifecycle hooks together). Lifecycle log asserts run after that reboot; sshd/embedded-hook checks stay config-only afterward. Dropped second OS→Base cycle.
3. **`77671` embedded app** — sanity keeps **v4 + SELinux/podman** only; Base teardown is a separate **slow** It (same checkpoint id).
4. **`87531` helm degraded** — sanity keeps Healthy→Degraded; Error-state path is a separate **slow** It (same checkpoint id). Sanity `delayBeforeFail` 20→15 (Error path stays at 5).
5. **`88004` helm auth** — sanity keeps helm+registry auth; rootful/rootless quadlet auth follow-ups are a separate **slow** It.
6. **`87279` greenboot** — sanity keeps rollback + no-retry; recovery OS→good image is a separate **slow** It. Stability 45s→20s; e2e health override 10/10/2→5/5/1.
7. **`79220` update schedule** — still on sanity; stability 45s→20s; cron wait windows 2m→1m.

### LPT packing (not deleted)

1. **`88425` journal persistent** — removed `sanity` label only (still runs; another full rollback cycle). Prefer re-adding to sanity once greenboot path is stably ≤5m and LPT allows.
2. **`83871` / `83847` OCI prefetch** — removed `sanity` only (~3m on LPT agent shard). Prefer re-adding once LPT ≤~12m.

### CI prep (not a test concession)

1. Agent qcow/bundle download runs **in parallel** with kind+Helm deploy (`run-e2e-tests.yaml`, detached `gh run download`).
2. Helm `BeforeSuite` uses `SetupWorkerHarnessWithoutVM` (VM still per-spec in BeforeEach).
3. cs9 sanity shard count **10→12** to improve LPT packing of remaining heavy agent specs.
4. **Opt A**: `build-agent-images-unified` only `needs: [compute-tag, build-rpms]`, not `build-images-and-charts` — it now runs concurrent with the container/chart builds instead of serialized after them (confirmed on run `30484730568`: `build-agent-images-unified` started at the moment `build-rpms` finished, ~3m before `build-images-and-charts` finished).

## LPT dir-narrow + spec trim (this round)

Data source: baseline run [30244616779](https://github.com/flightctl/flightctl/actions/runs/30244616779) (tip `9cd34be75`) vs. post-fix run [30484730568](https://github.com/flightctl/flightctl/actions/runs/30484730568) (tip `a5c603a0b`).

1. **Ginkgo dir-narrowing (validated, real but modest win)** — `compute_test_assignments` now emits `assignment-dirs.json` (node → unique suite dirs from assigned specs' `SuitePath`), and `run_e2e_tests.sh` uses it to pass only those dirs to `ginkgo run` instead of all 31 `test/e2e/*` packages.
   - Before: `ginkgo run ... ./test/e2e/...` compiled all 31 packages every shard — silent 210s (3.5m) compile phase, then ~28 zero-spec suites each costing ~1.3-1.5s launch overhead (~0.63m) — ~4.4m pure overhead per shard, repeated identically on all 17 shards.
   - After (node 4/cs10, 3 dirs: `agent`, `applications/rootless`, `hooks`): compile phase dropped to 136s (~2.3m); step `Run E2E Test Slice` (20m59s) minus JUnit-reported suite time (1109.0s = 18m29s) leaves ~2.5m residual overhead. Net: **~1.9m/shard** overhead cut (4.4m → 2.5m).
   - Caveat: smaller than the naive "31→3 packages ⇒ proportional" estimate (~3-4m). A large fraction of ginkgo's compile time is apparently fixed/shared (common internal packages, race-detector instrumentation) rather than linear in suite-package count.
2. **`78684` sanity/slow split (mechanically correct, but no net wall win)** — [test/e2e/parametrisable_templates/parametrisable_templates_test.go](test/e2e/parametrisable_templates/parametrisable_templates_test.go) `78684` was split into a `sanity`-only It (calls `setupTemplatedConfigFleet`) and a `slow`-only sibling It (calls the same setup, then does the target-revision/label-removal cycles). Confirmed in run `30484730568`'s JUnit: the `slow` sibling is correctly skipped under `sanity`/`sanity && agent && !microshift` filters.
   - However, the `sanity` It's measured time is **346.1s (5.77m)** — essentially unchanged from the pre-split baseline (5.67m). The two cycles moved to `slow` were not the dominant cost; `setupTemplatedConfigFleet`'s own single `WaitForDeviceNewRenderedVersion` (OS-image-alias swap + git/inline/HTTP config apply on a real VM) is. **Net sanity/PR-gate benefit ≈ 0.**
   - Side effect: a full/`slow`-inclusive run now pays `setupTemplatedConfigFleet`'s ~5.7m setup cost **twice** (once per It), since Ginkgo Its don't share state — a regression for the nightly/full suite that doesn't show up on the `sanity` PR-gate path. Worth revisiting (e.g. shared `BeforeEach`/`JustBeforeEach` context instead of two independent full-setup Its) or reverting.
3. **Net effect on wall/LPT**: max shard stayed ~26.9m (still cs10, though bin-packing reshuffled which specs land where run-to-run) and total wall stayed ~41-43m — **no material progress toward ≤30m from this round**, because Fix 2 didn't deliver as estimated and Fix 1's real ~1.9m/shard saving is within the run-to-run bin-packing noise.
4. **Next data-backed LPT candidate** (from run `30484730568`'s JUnit, not yet acted on): `test/e2e/applications/rootless` — *"Rootless applications covers all rootless checkpoints across quadlet, container, and compose app types"* — a single It costing **334.1s (5.57m)**, the largest individual spec on the current cs10 bottleneck shard. Same split pattern (keep one checkpoint type in `sanity`, move the rest to `slow`) is a plausible next cut, followed by the hooks lifecycle spec (236.2s / 3.94m) on the same shard.

## Known flakes

- **`89141` backup_restore**: diagnosed on run `30244616779` (job [89911680615](https://github.com/flightctl/flightctl/actions/runs/30244616779/job/89911680615)) as a transient ~60s network/API blip inside the shard's kind cluster (device agent(s) hit `connection reset by peer`/EOF talking to `agent-api`, then re-synced late), not a test-logic bug. **Recurred** on run `30484730568` (cs9 shard `e2e-test (1)`, same signature — device agent restarts after losing contact with the management server mid-test). Two occurrences now; if it keeps recurring, next step is investigating CI-runner resource pressure on the `flightctl-api` pod inside kind, not the test.

## Still needed for ≤30m (not done)

- Prebaked kind node / further prep cut (target job prep ~5m).
- Build critical path stay ≤~10m on stacked tip (currently ~14.9m: compute-tag + RPM build + agent images, though RPM build and container/chart builds already overlap per Opt A).
- Split the `test/e2e/applications/rootless` mega-spec (~5.57m, see above) the same way `78684` was (but validate the split actually reduces `sanity` cost before merging, given the `78684` lesson above).
- Re-measure after the next cut; other LPT candidates: agent BeforeSuite aux upload (~3m), residual 77671/79220, per-shard `BeforeSuite` VM bring-up (~2.9m, likely structural).

## Non-goals

- `GINKGO_PROCS` as the primary strategy.
- Deleting backup/restore or greenboot sanity coverage.
