# Delta-worker stacked PR split plan

This document is the working plan for splitting the current rollout-hold change
into two reviewable pull requests while preserving the existing stacked PRs.
It is an implementation reference, not a user-facing feature guide.

## Objective

Split the current top change into:

1. A lower foundation pull request for delta-worker persistence, services, and
   task plumbing.
2. An updated top pull request for the rollout and rendering behavior that is
   gated by delta preparation.

The foundation pull request must be independently buildable and safe to merge.
After it merges, `main` must remain operational with its existing rollout and
render behavior. The rollout behavior changes are enabled only by the updated
top pull request.

## Baseline

- Current top branch: `EDM-5223-hold-rollout-prepare-deltas`
- Current top commit: `a93414d66`
- Current pull request: `#3457`
- Current diff against `upstream/main`: 82 files
- No `AGENTS.md` changes are part of this plan.

The active stack currently has this order:

```text
main
  -> merged delta-worker refactor layers
  -> #3457  EDM-5223-hold-rollout-prepare-deltas
  -> #3459  EDM-5224-render-os-delta-hint
  -> #3464  EDM-5225-e2e-os-delta-hold
  -> #3514  EDM-5226-app-image-digests-prepare-deltas
  -> #3556  EDM-5227-render-app-delta-hints
  -> #3557  EDM-5228-apply-application-deltas
```

## Target stack

Insert one new branch immediately below the updated #3457 branch:

```text
main
  -> merged delta-worker refactor layers
  -> EDM-5223-delta-worker-foundation       (new lower PR)
  -> EDM-5223-hold-rollout-prepare-deltas   (updated #3457)
  -> #3459
  -> #3464
  -> #3514
  -> #3556
  -> #3557
```

The new branch targets `main`. The updated #3457 pull request targets the new
foundation branch. The pull requests above #3457 retain their branch names but
must be restacked after #3457 is rebuilt.

## Foundation branch scope

Branch: `EDM-5223-delta-worker-foundation`

The foundation branch contains the infrastructure required by the rollout
change:

- Delta model and dedicated generation, prepare, and join stores.
- Atomic pending-generation counting and join creation results.
- Store migration and removal of the obsolete shared delta store.
- Resource-oriented delta services and their generated mocks and tracing
  wrappers.
- Base prepare and generate task persistence and enqueue plumbing.
- Delta-worker composition and compatibility wiring needed by those services.
- Focused unit and integration coverage for the persistence and service layer.

The foundation branch must not change the existing rollout decision paths. In
particular, after merging this branch alone:

- Fleet updates still use the existing direct rollout path.
- Standalone device updates still use the existing direct render path.
- No new `PrepareDeltas` gating is required for normal fleet or device updates.
- Completion-only code must not be wired in a way that requires the upper
  branch to compile or start the worker.

Shared composition files, such as the delta-worker server and task handlers,
must keep their old behavior in this branch while receiving only the plumbing
hunks needed by the foundation.

## Updated top branch scope

Branch: `EDM-5223-hold-rollout-prepare-deltas`

The updated top branch contains the semantic behavior change:

- Fleet validation emits `PrepareDeltas` instead of starting rollout directly.
- Standalone device spec updates emit `PrepareDeltas` and delay rendering.
- Generation and prepare completion events route through dedicated handlers.
- Completion performs the conditional fleet rollout or device render resume.
- Fleet and device status mutations use the current identity/resource version.
- Waiting prepares can be failed by the periodic deadline task.
- Worker, periodic-checker, resource-service, and integration tests cover the
  new behavior.

The top branch may depend on the foundation APIs, but the foundation branch
must never depend on top-branch handlers, events, or resource behavior.

## Reconstructed file counts

The semantic reconstruction has now been completed locally. Counts below are
the actual unique paths in each branch-to-parent diff (rather than counts from
the old commit boundaries):

| Pull request | Comparison base | Changed files |
| --- | --- | ---: |
| Foundation | `upstream/main` | 35 |
| Updated #3457 | Foundation branch | 64 |

The updated #3457 tree is 83 files relative to `upstream/main` (the extra file
over the original 82 is this plan). The rebuilt upper layers add the following
incremental paths:

| Pull request | Comparison base | Changed files |
| --- | --- | ---: |
| #3459 / #5224 | Updated #3457 | 20 |
| #3464 / #5225 | #3459 | 24 |
| #3514 / #5226 | #3464 | 12 |
| #3556 / #5227 | #3514 | 9 |
| #3557 / #5228 | #3556 | 9 |

These counts are per pull request. Shared files appear in more than one diff
only when their foundation and behavior hunks are intentionally separated.

The reconstructed local branch names are:

```text
EDM-5223-delta-worker-foundation
EDM-5223-hold-rollout-prepare-deltas
EDM-5224-render-os-delta-hint
EDM-5225-e2e-os-delta-hold
EDM-5226-app-image-digests-prepare-deltas
EDM-5227-render-app-delta-hints
EDM-5228-apply-application-deltas
```

The branch relationships and per-layer counts remain the source of truth;
commit hashes will be rewritten when the stack is pushed.

## Reconstruction procedure

### 1. Freeze the baseline

- Confirm the working tree is clean.
- Record the current top commit and complete diff against `upstream/main`.
- Create a local backup reference for the current top branch.
- Do not modify merged stack layers or `AGENTS.md` files.

### 2. Build the foundation branch

- Create `EDM-5223-delta-worker-foundation` from the current stack bottom.
- Apply the foundation changes by semantic file and hunk selection.
- Do not cherry-pick the existing commit sequence.
- Keep existing rollout and render behavior unchanged.
- Run formatting, focused tests, full unit tests, and lint.

### 3. Validate the merge-safe foundation

Before opening or updating the upper pull request, validate the equivalent of
`main + foundation`:

- Compile all affected binaries and packages.
- Run the full unit-test and lint suites.
- Run relevant integration tests, including migration and delta-worker tests.
- Verify worker construction and startup do not require upper-branch symbols.
- Verify existing fleet rollout and standalone device rendering paths remain
  available without `PrepareDeltas` events.

### 4. Rebuild the updated #3457 branch

- Create the updated top branch from the foundation branch.
- Apply only the remaining rollout, rendering, completion, deadline, and
  integration-test hunks.
- Compare the resulting tree with the frozen baseline. No behavior or file
  should be lost unless it was intentionally assigned to the foundation.
- Run the full validation suite again.

### 5. Restack the remaining pull requests

- Insert the foundation branch below #3457 with `gh stack modify`.
- Update #3457 to target the foundation branch.
- Restack #3459, #3464, #3514, #3556, and #3557 in order.
- Push with the stack tooling and explicit lease protection.
- Verify every pull request base, changed-file list, and CI result.

The local reconstruction has completed the branch-by-branch restack. Remote
pushes and PR-base changes remain a separate, explicitly approved operation.

Validation completed locally:

- `make lint` passes on the rebuilt top branch.
- `go test ./test/integration/... -run '^$'` compiles all integration suites.
- The rebuilt top branch passes `go test ./internal/... ./cmd/... ./pkg/... ./api/...`.
- The #5227 branch had two pre-existing timing-sensitive `systeminfo` test
  failures in one run; its compilation and all delta-related packages passed,
  and the final #5228 tree passes the full command above.

## Completion criteria

- The foundation pull request is independently mergeable into `main`.
- `main + foundation` builds, starts, and retains existing rollout/render
  behavior.
- The updated #3457 pull request contains the delta-gated semantic change and
  has the foundation branch as its base.
- All upper stack pull requests are based on the updated #3457 branch.
- The reconstructed top tree matches the pre-split top tree.
- No `AGENTS.md` file changed.
- No branch is deleted until the stack and pull request diffs are verified.
