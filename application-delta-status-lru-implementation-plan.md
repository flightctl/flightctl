# Application delta status: digest-keyed LRU implementation plan

## Scope and constraints

Prepare application images independently of status reporting. After preparation succeeds, save its outcome under the final prepared manifest digest. On each status tick, look up outcomes using digests obtained from the runtime's current image observations.

This plan uses the current `t3code/fix-cri-helm-digest-deltas` branch as the implementation starting point and `main` as the comparison baseline. Preserve unrelated branch and worktree changes. Priority order: correctness, simplicity, then the smallest required change relative to `main`.

Only this plan file is authorized to change during the planning task. No implementation files or test files are changed, and no tests are run. During implementation, use focused unit tests and applicable lint checks. **Do not run integration tests, recommend their execution, or make them an acceptance criterion.** Runtime interoperability that cannot be established from source contracts and unit fixtures remains a verification limit for a later separately authorized step.

## Requirements

| ID | Requirement |
|---|---|
| R1 | Runtime status reports image references and manifest digests supported by actual runtime observations. Desired spec, registry and desired collection-cache digests never substitute for current evidence. |
| R2 | Artifact source digest, final prepared target digest and runtime-reported current digest remain distinct values. |
| R3 | A completed preparation outcome enters a digest-keyed LRU only after the final target pull succeeds and the final image is available in the required destination storage. |
| R4 | Status looks up the current reported digest. If A is running while B is prepared, status still looks up A; later it looks up B. |
| R5 | LRU retention is bounded. Misses, unknown observations and eviction affect reporting only. |
| R6 | Multiple reported image targets/digests are looked up separately and combined using the existing aggregate outcome rules. |
| R7 | Explicit hints are validated and attempted directly. An invalid, unavailable or failed hint leads to full pull, without discovery or selection of another artifact. |
| R8 | The no-hint path is separate. This plan assumes full pull, following the original request; independently required discovery remains in its own path. |
| R9 | Intermediate archives, source material and reconstructed targets may use other storage. Source access must match the chosen apply operation, and the final canonical pull must use the required destination. |
| R10 | Ordinary spec changes do not destroy applicable digest history. Context retirement requires confirmed evidence; persisted state has an explicit safe migration. |
| R11 | Cache behavior never changes pull selection, task readiness, scheduling, reconciliation or retry decisions. |
| R12 | Verification uses unit tests and lint only; unresolved real-runtime compatibility is recorded explicitly. |

## Current source findings

- `main` lacks this branch's application delta-status store and runtime digest reporting implementation. Continue the branch rather than rebuilding that feature.
- `internal/agent/agent.go:Agent.Run` registers the application exporter before the prefetch exporter. Keep `prefetchManager.Status` as the outcome-reporting hook.
- `PodmanMonitor.resolveWorkloadImage` currently inspects `event.Image`, which can be a retargeted mutable local tag rather than the image of `event.ID`.
- `digestFromKubernetesImageID` currently substitutes a pinned Pod-spec digest for opaque or missing runtime IDs. `collectPodImages` also includes nonrunning container specifications.
- `manager.Status` calls `addCachedParentImageDigests`. Its cache comes from desired dependency collection and can contain a newly prepared parent while the old application is active.
- `manager.addVolumeImageDigests` inspects a local image reference; that does not prove which immutable content was installed into a volume.
- Current prefetch task identity is owner plus reference. Targets from different runtimes can be coalesced before their type enters the target fingerprint.
- The worktree's `observeApplicationImageDigests` assigns the first reported digest to a new preparation result and deletes it on a later difference. This attributes B's outcome to A during an update and destroys B when B starts. Replace it entirely.
- Application monitors remove an application from their maps when removal is queued, before the removal handler succeeds. Mere absence from monitor status is insufficient retirement evidence.
- Existing spec/target reconciliation resets results broadly; this conflicts with retained per-digest history.

## Decisions required before implementation

These are prerequisites, not permission to guess runtime identities or enlarge implementation scope.

| Decision | Required resolution | Conservative behavior until resolved |
|---|---|---|
| P1: comparable image identity | Verify supported Podman container-inspect fields, immutable pull-result identities and CRI local image-status fields from official contracts or existing fixtures. Distinguish platform manifest, index and config digests. | Unproven or incomparable identity is unknown. Never copy a desired or source digest into the prepared/reported field. |
| P2: active parent/volume semantics | Decide whether status strictly covers running workload images, or also actual installed parent/volume dependencies. The smaller recommended route is strict workload reporting, but removal of existing dependency reporting requires this decision. | Do not inject desired parent cache or mutable volume-tag inspection into current status. |
| P3: storage identity and source access | Establish the store read by each apply operation and the effective image-service destination used by final pull/inspection. Determine how current runtime observations identify that same destination independently of desired settings. | Ambiguous status context is a miss/unknown. Inaccessible apply source leads to full pull. |
| P4: retirement evidence | Identify lifecycle completion and runtime snapshot/event evidence sufficient to prove an old context is no longer desired or active. Account for queued removals, failed actions, rollouts, reboot and incomplete watches. | Retain contexts on unknown evidence. Never retire from status absence after queued removal alone. |
| P5: capacity and context bound | Select a finite per-context digest capacity and ensure context count is bounded by live ownership or a separately reviewed cache limit. Consider prolonged unknown runtime observations. | Capacity **8 is a proposal**, not a user requirement. Do not claim a total memory bound solely from a per-context capacity. |

Resolve P1-P4 and record the capacity/context-lifetime choice before production edits. If findings require files or methods not listed below, revise the inventory first. The conditional inventory is a bounded description of known implementation routes, not a claim that unresolved alternatives already have a final exhaustive patch list.

## Implementation prerequisite decisions

- P1: Podman uses container `Image` (immutable local image ID), then that image's `Digest`; preparation inspects the immutable successful-pull ID. Kubernetes accepts matching repository-qualified runtime image IDs from running ordinary, init and ephemeral containers and leaves opaque/missing IDs unknown. CRI local identity must be unambiguous and proven comparable; unsupported local identity remains unknown.
- P2: Select strict workload reporting. Desired parent cache and volume tag inspection do not contribute to current image status. Keep desired extraction/prefetch unchanged.
- P3: Scope Podman by effective user/default local storage. CRI requires a concrete configured image-service endpoint; missing/ambiguous endpoint is unknown for reporting. Containerd import/mount uses the image endpoint and `k8s.io`. CRI-O source access through default containers/storage is unproven and falls back directly to full pull.
- P4: A successful remove lifecycle action with its captured lifecycle identity confirms retirement only when the application is no longer desired; queued removal and status absence do not. Persist a monotonic lifecycle identity; recreation after confirmed removal gets another identity. Current context metadata is carried privately through the shared concrete store, using monitor ownership rather than desired targets. Incomplete watch state suppresses aggregation.
- P5: Eight digest entries per context and 256 contexts globally. Old contexts may be evicted as cache capacity policy, independently of lifecycle retirement; cache eviction only affects reporting. Lifecycle ownership metadata is retained until confirmed removal and contains no outcome collections.
- Add `internal/agent/client/kube.go:ListPods` and its `kube_test.go` fixtures: `KubernetesMonitor.currentImageObservations` uses a successful PodList snapshot to establish Kubernetes image-set completeness; watch events alone do not.
- Add `Podman.TagPreparedImage` and `CRI.TagContainerdImage`: after a frozen canonical pull of a mutable target, retain its original runtime reference as an alias of the final immutable image; alias failure prevents success recording.
- Inventory selects conditional store wiring in `agent.go`, manager/monitor lifecycle/context metadata, strict removal of parent/volume reporting, client immutable digest methods and containerd endpoint routing. Also update `docs/user/references/agent-architecture.md` to remove obsolete cache semantics, and unit fixtures affected by strict reporting. No generated interface changes are needed.

## LRU ownership, entries and lookup

### Recommended context

Use one digest-keyed LRU for each application/target/destination context. The context includes application lifecycle identity, normalized target reference, runtime/storage type, effective owner, and destination identity where applicable (for example CRI image-service endpoint and containerd namespace).

Normalize reference spelling, but preserve tag and pinned-reference semantics. Do not include the resolved mutable-tag digest or the entire desired spec fingerprint in the context: successive digests must coexist. Reference changes create another context; the old one remains available while its reference remains active.

This scope preserves outcome provenance. A global digest cache could attribute a Podman preparation to CRI or another user. A cache per application alone could attribute one target's preparation history to another target with the same digest. A cache per storage alone still loses application and target provenance. Shared physical preparation may insert the same completed result into each associated application's appropriate context.

Use the concrete `deltastatus.Store` as the authoritative owner. Provide an in-memory store when persistence is absent so prefetch does not maintain a second result implementation. For the proposed small capacity, an ordered entry collection with digest lookup and move-to-most-recent behavior is sufficient; avoid a new dependency, duplicate cache indexes and a new generic cache abstraction.

### Three digest values

| Value | Source | Use |
|---|---|---|
| Artifact source digest | Delta artifact source annotation | Find source content accessible to the apply operation. |
| Final prepared target digest | Immutable local identity of the image after successful final pull into the destination | LRU key for the completed outcome. |
| Runtime-reported current digest | Immutable image identity observed for a running workload | LRU lookup key during status. |

The resolved desired target reference is execution metadata. An index digest, reconstruction digest or config ID is not automatically comparable with a reported platform manifest. Never populate one of the three values from another. A local alias may be used only when its identity relationship is proven; do not use registry resolution solely to validate/report cache entries.

### Entry and insertion contract

An entry contains its final prepared manifest digest, a cloned completed `DeviceDeltaApplyStatus` (outcome and optional fallback reason), and recency position. Context identity is held by the enclosing cache. Task/spec generation guards remain execution safeguards, not history namespaces.

- Successful delta apply plus successful canonical final pull and destination availability: insert `Applied` under the final prepared digest.
- Failed/unusable explicit hint plus successful full pull: insert `Fallback` with its reason.
- No-hint successful full pull: insert `NotUsed`.
- Final pull failure: insert nothing.
- Successful preparation whose manifest identity remains unknown: do not insert under an empty or inferred digest; log/report the identity limitation without failing otherwise successful reconciliation.
- A local presence/digest check without a final pull does not manufacture an entry or overwrite an earlier result with `NotRequired`. Keep existing entries. Do not pull solely to fill the status cache. Existing `NotRequired` behavior and documentation must be revised consistently with this insertion rule.
- Another successful preparation of the same digest updates its entry with the latest completed outcome and moves it to most recent.
- A new digest beyond capacity evicts the least recently used digest. A successful status hit refreshes recency; misses and unknown observations do not.

### Status contract

`prefetchManager.Status` clears this tick's `LastDelta`, resolves context using current runtime identity evidence, and looks up every distinct reported `(context, current digest)` pair. It never chooses the latest prepared result merely because it belongs to the application.

| Observation | Selection | Cache effect |
|---|---|---|
| Running A; A cached | Select A's result. | Touch A. |
| B prepared while A runs | Continue selecting cached A, if present. | Keep B for a later tick. |
| Running B on a later tick | Select B's saved result. | Touch B. |
| Known digest absent or evicted | No result for that image. | Do not infer a result, delete another entry or change prefetch. |
| Digest/context missing, opaque, invalid or incomparable | Eligibility unknown. | Preserve entries without touching them. |

Aggregate the actually reported current image set, not the latest desired target set. Desired collection incompleteness, pending B, changed task generation and `deltaTargetsScheduled` must not hide a valid cached result for running A. Conversely, newly prepared B cannot contribute until status reports B.

Deduplicate identical `(context,digest)` pairs. If a rollout reports A and B and both are cached, include both outcomes. Preserve the existing aggregate rules: `Applied` combined with `Fallback` or `NotUsed` is `Partial`; representative fallback reasons remain deterministic. If any relevant running-image observation is unknown or lacks an entry, omit the aggregate for this tick while retaining all cache entries. No relevant running images means no aggregate.

Preserve an unknown observation alongside known digests for the same reference. Current `application.collectImageDigests` otherwise drops that unknown when another container reports a known digest. Completeness of the runtime image set itself must be established under P1/P4; a successful read of an incomplete monitor cache is not proof that every running image was observed.

## Storage and pull paths

| Step | Minimum safe guarantee |
|---|---|
| Download delta archive | Apply process can read it. It need not occupy final image storage. |
| Locate source | The chosen apply implementation can access immutable source content matching the source annotation. A check against a different store is insufficient. |
| Reconstruct | Output is readable by the next import/transfer step. Its digest and store may differ from the final image. |
| Canonical final pull | Uses the required runtime, effective user and image-service configuration. Freeze the intended resolved execution target across artifact validation and pull. |
| Record result | Final image is available in that destination and its comparable manifest identity is established there. |

Do not require every intermediate to be copied into the final store. Such copies may help reuse blobs but are not a correctness prerequisite. Current containerd mounting/import uses a socket and `k8s.io`; verify image-service endpoint selection. Current CRI-O import uses default root containers/storage: establish actual apply-source access rather than assuming the CRI existence check proves it. If source access cannot be established, fall back to the destination's full pull.

Explicit hint handling validates reference, artifact type, source annotation, target applicability and apply-source access. Any explicit-hint failure goes straight to full pull, without `ListReferrers` or another artifact candidate. Missing hint uses full pull under R8's stated assumption. Keep any independently required discovery separate, including OS behavior; delete an application discovery helper only if it has no independent caller.

## Context lifetime and persistence

- New digest for unchanged application/target/destination: preserve prior entries until LRU eviction.
- Hint, environment, restart or unrelated spec changes: retain applicable histories.
- Reference or destination changes: create an isolated context; keep the prior context while it is active.
- Spec generation prevents stale insertion but does not invalidate already completed history for an unchanged context.
- Retire contexts only when they are no longer desired or active and the required lifecycle/runtime evidence is confirmed. Failed/incomplete status and queued removal are not sufficient evidence.
- Application removal/recreation must not inherit an obsolete lifecycle's contexts. Persist enough lifecycle/context identity to distinguish them across restart.
- Bound context retention as well as per-context entries. The exact context bound/cleanup mechanism must be settled under P4/P5; unknown observations must not be misrepresented as proof of retirement.

Persist context identity, digest entries and recency order. Load entries in order, reject malformed identities/digests, deduplicate keys deterministically and enforce the selected capacity. Snapshot methods clone entry collections and fallback reason pointers.

Replace the worktree's silent version-1 JSON change from `contentDigest` to `reportedDigest` with an explicit schema migration. Preserve OS state. Discard legacy application results whose final prepared manifest identity is unproven; neither old desired `contentDigest` nor first-reported `reportedDigest` is safe to reinterpret. Do not seed caches from runtime observations during migration. Unknown future schema versions must remain safely ignored without failing agent startup.

Persist insertion, update, eviction and confirmed retirement. Batch status hits, persist final recency order only when it changes, and keep disk-write errors from affecting reconciliation. Report/log durability limitations rather than pretending a failed save persisted newer state. Serialize cache mutation through the store's lock; avoid opposing lock orders between application, prefetch and store locks.

## Ordered implementation tasks

1. **Resolve prerequisites and inventory choices (R1, R2, R9, R10).** Establish P1-P5 and select the strict-runtime or active-dependency route. Revise conditional inventory before coding if findings demand another route.
2. **Implement scoped bounded histories and explicit migration (R3-R5, R10).** Add context/entry representations and concrete store lookup/insertion APIs. Replace single-result storage and supply an in-memory store for no-persistence construction.
3. **Correct runtime digest collection and completeness (R1, R2, R6).** Resolve Podman by immutable container/image ID, remove Kubernetes spec fallback, collect running states and preserve unknown observations. Remove desired-cache enrichment from current status.
4. **Make destination and apply-source identity explicit (R2, R9).** Stop cross-runtime task coalescing, obtain current/destination identity consistently, and verify access in the store actually read by apply. Intermediates may remain elsewhere.
5. **Separate hint/no-hint execution and save only completed outcomes (R3, R7-R9).** Freeze execution target, full-pull without hints, directly fall back after failed hints, obtain final local digest after successful pull and insert it under stale-task guards.
6. **Replace reporting with current-digest lookup (R4-R6, R11).** Remove first-observation assignment, desired/task-based status selection and duplicate result restore/merge machinery. Aggregate only current observations and cache hits.
7. **Implement confirmed context retirement (R5, R10, R11).** Carry required active-context/lifecycle evidence without changing user API fields. Preserve old active contexts during updates and reject lifecycle reuse after confirmed removal.
8. **Update contract unit tests and status documentation (R1-R12).** Replace obsolete tests, add sequence/eviction/migration coverage, and document unknown/miss behavior. Unit and lint validation only; no integration-test execution.

## File-by-file implementation inventory

Status labels: **Required** for the selected core design; **Conditional P1/P2/P3/P4** when the prerequisite chooses that route; **Conditional compatibility** for existing tests/callers affected by a changed private representation. Names marked **add** are proposed exact names, not claims that they already exist. The inventory covers the expected core patch and the known conditional routes. It is not an unconditional final list for unresolved runtime/dependency alternatives: update it before implementation if a prerequisite identifies additional files. Do not silently edit unlisted files.

### Required production files

#### `internal/agent/device/deltastatus/store.go`

- Change `persistedState`, `applicationState`, `applicationImageResult`, `ApplicationSnapshot` and `ApplicationResult`: represent scoped ordered digest histories and cloned completed statuses; eliminate first-reported/desired digest fields.
- Add `ApplicationContext`, `ApplicationLookup` and `ApplicationLookupResult`: concrete context and batch lookup data, including reported digest; no caller-side testing interface.
- Add `applicationHistory` and the proposed `applicationHistoryCapacity` constant: ordered bounded LRU representation; value 8 is a proposal subject to P5.
- Add `NewInMemory`: same cache semantics without disk persistence; keep a single implementation.
- Change `New`: explicit legacy migration, OS preservation, context/digest validation, duplicate handling and capacity enforcement.
- Change `ReconcileApplicationSpecs` and `ReconcileApplicationTargets`: stop clearing compatible history on every spec/target fingerprint change; track desired ownership separately from confirmed active ownership.
- Change `RecordApplicationResult`: accept context plus final prepared digest; insert/update completed result and evict by recency.
- Add `LookupApplicationResults`: batch digest lookup with recency update and cloned results; no insertion on miss/unknown.
- Change `ApplicationResults` and `ApplicationSnapshot`: expose cloned scoped histories for diagnostics/tests; status need not scan desired snapshots to choose an outcome.
- Remove or repurpose `ClearApplicationResult`: remove mismatch-driven invalidation; explicit removal becomes context retirement.
- Add `RetireApplicationContexts`: retire only confirmed obsolete contexts; exact confirmation payload follows P4.
- Change `saveLocked`: persist new schema/order; support in-memory mode; coalesce changed-order writes.
- Keep `SetOSTarget`, `OSResult`, `RecordOSResult` semantics unchanged; `cloneStatus` remains the status cloning helper.

#### `internal/agent/device/dependency/dependency.go`

- Change `imageRef`, `prefetchTask`, `prefetchManager` and, if needed, `OCIPullTarget`: carry effective destination identity before coalescing; maintain desired/active context metadata separately from saved histories.
- Change `NewPrefetchManager`, `WithDeltaStatusStore`: initialize/use one concrete store, including in-memory default; preserve public construction behavior.
- Change `BeforeUpdate`, `applicationDeltaSpecKeys`, `applicationTargetIdentities`, `applicationImageTargetKey`, `applicationImageTargetKeyFor`: separate stale-execution fingerprints from long-lived context identity; avoid clearing history solely because desired spec changes.
- Change `Schedule`, `schedule`, `prepareTask`, `processTarget`, `pull`, `preparePullClients`, `pullByOCIType`, `pullAutoDetectedOCIType`: propagate destination identity and immutable successful-pull identity consistently.
- Change `pullApplicationImage`, `pullCRIImage`, `applicationDeltaFallback`, `applicationCRIDeltaFallback`: no-hint full pull, explicit-hint direct fallback, canonical destination verification and final prepared digest capture before recording.
- Change `recordDeltaFallback`, `recordDeltaApplied`, `recordDeltaNotUsed`, `recordDeltaApplyResult`, `recordDeltaApplyResultLocked`: accept the final prepared digest and insert only after successful completed preparation. Do not preserve an obsolete value when a newer successful preparation of the same digest should replace it.
- Change `checkCachedApplicationTask` and relevant branches of `prepareTask`: preserve readiness checks but stop creating/upgrading cache entries from presence-only checks. Registry desired-digest checks may remain where independently necessary to decide readiness, never for status-cache validity.
- Change `Status`: context resolution, deduplicated current-digest batch lookups and complete current-set aggregation. Remove `deltaTargetsScheduled` as a status gate; keep it only if prefetch still needs it independently.
- Remove `observeApplicationImageDigests`: no first-observation assignment or mismatch deletion.
- Remove/replace `applicationDeltaApplyStatuses`, `collectApplicationDeltaApplyResults`, `persistedApplicationDeltaApplyStatuses`, `persistedApplicationDeltaResults`, `applicationDeltaResultFromSnapshot`, `currentApplicationDeltaResult`: saved outcome selection comes from current-digest lookup, not latest desired targets/tasks.
- Remove `restoreApplicationDeltaResults`, `mergeApplicationDeltaResults` and duplicate `deltaApplyResults` state when the store becomes authoritative.
- Change `aggregateApplicationDeltaApplyResults` to consume the selected current `(context,digest)` results while preserving aggregate semantics and deterministic fallback reason selection.
- Change `hasChangedTasks`, `cleanupStaleTasks`, `cleanupChangedTasks`, `samePrefetchTarget`, `Cleanup` and `imageRef.String` only to propagate the revised task identity without changing reporting-independent readiness/retry behavior.
- Change `applicationImageDigest`/digest validation as needed to reject invalid or incomparable cache keys; never treat opaque config IDs as manifests.
- **Conditional P3:** `inspectApplicationDeltaCandidate`, `applyContainerdImageDelta`, `applicationOCIDelta`, `applicationDeltaReadWriter` and `normalizedCRIImageReferences`: prove apply-source access, freeze target and route storage correctly.
- **Conditional P2:** `pullApplicationArtifact`, `applicationArtifactDeltaFallback`: if installed dependencies remain reportable, capture their own final immutable identity after successful pull; otherwise keep their execution but exclude them from runtime aggregation.
- **Conditional independent discovery:** remove `selectApplicationDeltaCandidate` and `applicationDeltaPayloadSize` only if no independent application discovery caller remains. Keep OS discovery unchanged.

#### `internal/agent/device/applications/applications.go`

- Change `application.collectImageDigests`: running observations only under the strict route; emit unknown alongside known digests for the same reference; deterministic deduplication.
- Change `application.Status`: propagate observation completeness without fabricating digests.
- **Conditional P2:** `application`, `NewApplication`, `NewHelmApplication`: separate actual installed parent/dependency identity from desired provider identity if active dependencies remain reported; otherwise remove parent image contribution to runtime status.
- **Conditional P3/P4:** `WorkloadImage`, `Workload`, `application.CopyWorkloadsFrom`: carry immutable observation context through updates if application-level context is insufficient. Extend `Application` only if the selected concrete design truly requires it; see generated-file condition below.

#### `internal/agent/device/applications/podman_monitor.go`

- Change `resolveWorkloadImage`, `updateApplicationStatus`, `inspectContainer`: derive current digest from `event.ID` and immutable image identity, reuse only valid identity-matched evidence, retry unknowns.
- Change `Status`: preserve completeness and the context of the runtime observations; do not equate a pending removal's map absence with confirmed retirement.
- **Conditional P3/P4:** `PodmanMonitor`, `QueueUpdate`, `QueueRemove`, `executeActions`: retain pending/active context and lifecycle evidence until successful completion; preserve failure behavior.

#### `internal/agent/device/applications/kubernetes_monitor.go`

- Change `collectPodImages`, `updatePodStatus`: use running ordinary/init container status evidence, preserve unknowns and multiple observed digests.
- Change `kubernetesContainerStatus` only for verified additional runtime identity fields needed by P1.
- **Conditional P1/P3:** `handlePodEvent`, `KubernetesMonitor`, `NewKubernetesMonitor`: pass context/local immutable identity lookup into event processing when opaque IDs can be resolved safely; otherwise retain unknown without a new dependency.
- **Conditional P4:** `removeWorkload`, `Remove`, `Update`, `executeActions`: provide confirmed deletion/lifecycle evidence and carry active contexts through rollouts.

#### `internal/agent/device/applications/image_digest.go`

- Change `digestFromKubernetesImageID`: remove desired pinned-reference fallback for opaque/missing runtime ID; retain qualified matching-runtime reference handling.
- Remove `digestFromReference` from this file if it becomes unused; desired execution parsing elsewhere remains independent.

#### `internal/agent/device/applications/manager.go`

- Change `Status`: remove `addCachedParentImageDigests`; pass only actual current observations to reporting and, under the P3/P4 route, save current context/completeness metadata for the later prefetch hook.
- Remove `addCachedParentImageDigests`; desired collection cache remains in use for prefetch.
- Change `collectApplicationStatuses`: preserve unknown/completeness information; conditional parent/volume route determines additional active identities.
- **Conditional P2:** `populateCachedParentImageDigest`, `addVolumeImageDigests`, `volumeImageKey`, `volumeImageDigests` fields, `invalidateVolumeImageDigests`, `finishVolumeImageUpdates`: remove desired/local-tag enrichment under the strict route, or replace it with installed identity evidence under the retained-dependency route. Update call sites in `Ensure`, `Remove`, `Update`, `AfterUpdate` accordingly.
- **Conditional P3/P4:** `manager`, `NewManager`, `Ensure`, `Update`, `Remove`, `AfterUpdate`: carry known active contexts and confirm lifecycle completion. Prefer a concrete shared store option and preserve old constructor calls with an optional argument.
- **Conditional P3/P4 add:** `ManagerOption`, `WithDeltaStatusStore`, `recordActiveApplicationContexts`: publish trustworthy context/lifecycle metadata only; do not perform LastDelta aggregation here.

#### `internal/agent/client/podman.go`

- Change `PodmanInspect` using verified container image fields from P1; do not invent field contracts.
- Add `Podman.ContainerImageDigest`: inspect immutable container image identity and return its comparable manifest digest or unknown/error.
- Reuse `Podman.ImageDigest` against an immutable successful-pull image ID for prepared identity; change parsing only if P1 requires validation.
- **Conditional P1:** `Podman.Pull`, `Podman.pullImage`: expose a verified immutable pull-result identity if current trimmed stdout is insufficient; preserve existing callers or provide a concrete additional method.
- **Conditional P3 add:** `Podman.StorageIdentity`: local effective store/user identity if configuration can change the default destination; no registry operation.

#### `internal/agent/client/cri.go`

- Add `CRI.ImageManifestDigest`: local lookup by immutable image ID and verified comparable manifest evidence; no desired tag/registry fallback.
- Change `ImageRepoDigests` parsing only as required to share the verified local identity extraction; multiple index/manifest aliases must not be chosen by arbitrary list order.
- **Conditional P1:** `CRI.Pull`, `CRI.pull`: expose verified immutable successful-pull ID when needed.
- **Conditional P3:** `CRIRuntimeInfo`, `RuntimeInfo`, `containerdEndpoint`, `executeContainerd`: distinguish image-service destination from runtime service; maintain socket/namespace consistency.
- **Conditional P3 add:** `CRI.ImageServiceIdentity`: obtain the effective local image-service context used by final pull and status observation.
- **Conditional P3:** `MountContainerdImage`, `ImportContainerdImage`, `UnmountContainerdImage`: change only if source/destination findings require corrected routing or a distinct source-access step.

### Required and compatibility unit-test files

All proposed tests use public behavior where practical, table-driven cases, `testify/require` and the project's existing executer mocks. Package-private helpers may be retained for fixture construction; do not test LRU implementation details instead of observable contracts.

#### `internal/agent/device/deltastatus/store_test.go`

- Replace `TestStorePersistsDeltaResultsAndClearsChangedTargets` with **add** `TestStorePersistsApplicationDigestHistory`: retention across unrelated spec changes and restart, OS state unchanged.
- Replace `TestClearApplicationResultIgnoresSupersededTargets` with **add** `TestStoreRetiresOnlyConfirmedApplicationContexts`.
- Replace `TestRecordApplicationResultPreservesUnknownReportedDigest` and `TestStoreLoadsPersistedReportedDigest`: first-observation schema is obsolete.
- **Add:** `TestStoreApplicationHistoryLRU`, `TestStoreApplicationContextIsolation`, `TestStoreApplicationLookupUnknownAndMiss`, `TestStoreApplicationHistoryMigration`, `TestStoreApplicationHistoryLoadValidation`, `TestStoreApplicationHistorySaveFailure`, `TestStoreInMemoryApplicationHistory`. Cover insertion/update/touch/eviction, capacity enforcement, duplicate keys, cloned snapshots, unsafe legacy rejection, persisted order and nonblocking persistence failures.

#### `internal/agent/device/dependency/delta_status_test.go`

- Replace `TestApplicationDeltaStatusUsesReportedDigests` with **add** `TestApplicationDeltaStatusLooksUpReportedDigest`: A cached, B prepared, A tick, unknown tick, B tick, restart and B tick.
- Replace `TestApplicationDeltaStatusInvalidatesOnlyReportingApplication` with **add** `TestApplicationDeltaStatusScopesHistories`: per-application/target/runtime/user isolation without mismatch deletion.
- **Add:** `TestApplicationDeltaStatusAggregatesCurrentDigests`, `TestApplicationDeltaStatusRequiresCompleteCurrentObservations`, `TestApplicationDeltaStatusEvictionDoesNotChangePrefetch`, `TestApplicationDeltaStatusRetainsOldActiveReference`, `TestApplicationDeltaStatusDoesNotUseDesiredTargetSet`, `TestApplicationDeltaStatusContextUnknown`. Include rollout A/B, duplicates, order independence, misses and known-plus-unknown observations.

#### `internal/agent/device/dependency/delta_prefetch_test.go`

- Change `TestApplicationDeltaPrefetch`: successful insertion uses final local digest; failed pulls do not insert; no-hint path has no discovery.
- Change `TestCachedApplicationImageReportsNotRequiredWhenDigestMatches`: replace the synthetic-history expectation with preserved existing history/no insertion on a no-pull check; rename accordingly.
- Change `TestApplicationDeltaStatusWaitsForTargetsToBeScheduled`, `TestApplicationDeltaStatusWaitsForAllTargetsToHaveResults`, `TestApplicationDeltaStatusUsesCurrentTargetReferences`: replace desired-target scheduling/completeness assumptions with current-observation lookup/completeness.
- Change `TestApplicationDeltaStatusPersistsAcrossRestartAndClearsForChangedSpec`: retain compatible history across ordinary spec changes; clear only confirmed retired/incompatible contexts; rename accordingly.
- Change `TestBeforeUpdatePreservesCurrentDeltaResultsAcrossRetries`: assert histories remain intact and stale executions cannot insert.
- Change `TestAggregateApplicationDeltaApplyResults`: aggregate selected current `(context,digest)` outcomes.
- Change `TestApplicationTargetIdentitiesTrackTargetsWithoutDeltaHint`: verify context identity includes runtime/storage and retains no-hint application ownership.
- **Add:** `TestApplicationDeltaPrefetchRecordsCanonicalPreparedDigest`, `TestApplicationDeltaPrefetchFailedFinalPullDoesNotRecord`, `TestApplicationDeltaPrefetchMissingHintFullPullsWithoutDiscovery`, `TestApplicationDeltaPrefetchPreparedIdentityUnknown`, `TestApplicationDeltaPrefetchStorageContextsDoNotCoalesce`.
- **Conditional compatibility:** `TestCachedApplicationImageWithStaleDigestAttemptsHintedDelta`, `TestOCIArtifactWithDeltaHintFallsBackToFullArtifactPull`, `TestPrepareTaskReusedCompletedTargetKeepsCacheWhenDigestCheckIsUnavailable`, `TestPrepareTaskCachedDigestChecks`, `TestPrepareTaskCompletesExistingImageWhenDigestCheckIsUnavailable`, `TestPrepareTaskSkipsExistingArtifactWhenDeltaHintExists`, `TestBeforeUpdateKeepsGenerationForEquivalentDesiredSpecs`, `TestPrepareTaskReusedCompletedNoHintTargetOnlyRechecksOnNewGeneration`, `TestApplicationDeltaSpecKeyIgnoresOnlyRenderedStatusAndDeltaHints`, `TestApplicationDeltaSpecKeyUsesResolvedNameWhenNameIsOmitted`, `TestApplicationDeltaPrefetchRunsAsRunAsUser`, `TestApplicationDeltaPrefetchRunAsDeltaImportFailureFallsBackToFullPull`: update context/store fixtures and final immutable-inspection expectations; preserve readiness and user-routing contracts.

#### `internal/agent/device/dependency/delta_cri_test.go`

- Change `TestApplicationDeltaPrefetchCRIRefreshesRegistryReference`: assert final digest/context and successful destination availability; intermediate digest/store may differ.
- Change `TestApplicationDeltaCRICachedImageReportsNotRequiredWhenDigestMatches`: presence-only checks do not insert history; preserve existing entry.
- Change `TestApplicationDeltaCRIHintedCandidateReportsNotUsedWhenDeltaClientIsUnavailable`, `TestApplicationDeltaCRIPinnedImageReportsNotUsedWhenDeltaSourceIsMissing`: align outcomes with explicit-hint failure and successful fallback, not pre-pull recording.
- Change `TestApplicationDeltaInvalidHintFullPullsWithoutDiscovery`: include unavailable/failed hint and no alternate selection.
- **Add:** `TestApplicationDeltaCRIMissingHintFullPullsWithoutDiscovery`, `TestApplicationDeltaCRIRecordsFinalPreparedManifest`, `TestApplicationDeltaCRIFailedFinalPullDoesNotRecord`, `TestApplicationDeltaCRIFinalDestinationRequired`, `TestApplicationDeltaCRISourceAccessUsesApplyStore` (last two verify command routing/failure contracts only).
- **Conditional independent discovery:** `TestSelectApplicationDeltaCandidateChoosesSmallestAvailablePayload`, `deltaCandidateManifest`: retain for an independent discovery path, or remove with unused helper. Do not recast discovery as hinted fallback.

#### `internal/agent/device/dependency/cached_task_test.go`

- **Conditional compatibility:** `TestCachedTaskInspectionAllowsWorkerCompletion`, `TestCachedTaskInspectionBoundsGenerationRetries`, `TestCachedAutoArtifactRemainsPresent`: update task identity/store fixtures; retain worker concurrency and bounded-check behavior; no cache-effect-driven pulls.

#### `internal/agent/device/dependency/dependency_test.go`

- Change `TestEnsureScheduled`, `TestBeforeUpdate`, `TestTargetChangeCleanup`: add runtime/storage task identity coverage.
- Change `TestStatus`: preserve progress status independent of application history.
- **Conditional compatibility:** `TestIsReady`, `TestBeforeUpdateCriticalDiskAlert`, `TestStatusMessage`, `TestPullSecretCleanup`, `TestCleanupPartialLayers`, `TestSetResultAfterCleanup`, `TestSelectiveCleanup`, `TestDetectOCIType`: adjust any affected task fixtures/constructor expectations only, preserving behavior.

#### `internal/agent/device/dependency/delta_test.go`

- Change `TestDeltaTargetSelectionUsesHintsAndApplicationNames` only if context-aware coalescing requires fixture updates; explicit hint selection remains deterministic.
- **Conditional independent discovery:** `TestApplicationDeltaPayloadSize`: remove only with the unused application-discovery sizing helper, otherwise retain unchanged.

#### `internal/agent/device/applications/applications_test.go`

- Change `TestCollectImageDigests`: running-only/selected dependency semantics, multiple digests, duplicate references and unknown alongside known.
- Change `TestApplicationStatus`: completeness and nonrunning-image behavior.
- **Conditional P3/P4:** `TestWorkloadImageSnapshotsAreCopied`, `TestNewAppFromProvider`: context metadata is cloned and originates from actual installed/runtime evidence.
- **Add:** `TestCollectImageDigestsPreservesUnknownAlongsideKnown`, `TestApplicationStatusDoesNotUseDesiredParentDigest`.

#### `internal/agent/device/applications/podman_monitor_test.go`

- Change `TestPodmanMonitorTracksWorkloadImageDigest`: immutable container image lookup, retargeted local tag, identity reuse and unknown retry.
- Change `mockPodmanInspect`: verified immutable image fields in fixtures.
- **Conditional compatibility:** `TestListenForEvents`, `TestApplicationAddRemove`, `TestPodmanMonitorStatusAggregation`: update affected inspection/context fixtures.
- **Conditional P4:** `TestPodmanMonitorMultipleAddRemoveCycles`: queued removal/failure/recreation must not retire history prematurely.
- **Add:** `TestPodmanMonitorRunningImageSurvivesTagRetarget`, `TestPodmanMonitorUnknownDigestRetries`, `TestPodmanMonitorRemovalRequiresCompletedLifecycle` (P4).

#### `internal/agent/device/applications/kubernetes_monitor_test.go`

- Change `TestKubernetesMonitorUpdatePodStatusCollectsImageSnapshot`: running-state evidence, missing/opaque IDs, running init containers, completed init exclusion and rollout A/B.
- Change `TestKubernetesMonitorUpdateAfterRestart` if active context metadata must survive workload copying.
- **Conditional P1/P3:** `newTestKubernetesMonitor`, `testKubeCLIClients.CRI`: provide local immutable-ID lookup fixtures only when selected.
- **Add:** `TestKubernetesMonitorUnknownImageIDDoesNotUseSpecDigest`, `TestKubernetesMonitorCollectsOnlyRunningImageEvidence`, `TestKubernetesMonitorRolloutRetainsMultipleDigests`, `TestKubernetesMonitorRetirementWaitsForConfirmedRemoval` (P4).

#### `internal/agent/device/applications/image_digest_test.go`

- Change `TestDigestFromKubernetesImageID`: opaque/missing ID with a pinned declared reference stays unknown; qualified matching runtime manifest remains supported.

#### `internal/agent/device/applications/manager_image_digest_test.go`

- Replace `TestAddCachedParentImageDigests` with **add** `TestManagerStatusDoesNotReportDesiredCachedParentDigest` through manager status behavior.
- **Conditional P2 add:** `TestManagerStatusReportsInstalledParentIdentity` only if active parent reporting is retained and an installed-source contract is resolved.

#### `internal/agent/device/applications/manager_test.go`

- Change `TestManagerRemoveApplication`, `TestManagerUpdateAfterRestart` for the selected P4 retirement/context route.
- **Add:** `TestManagerConfirmsContextRetirementAfterSuccessfulActions`, `TestManagerFailedActionsRetainActiveContexts`, `TestManagerStatusPreservesObservationContext` (P3/P4).
- **Conditional P2:** `TestAddVolumeImageDigests`, `TestVolumeImageDigestCacheLifecycle`, `TestVolumeImageDigestCacheRetries`, `TestVolumeImageDigestCacheRunAs`, `TestVolumeImageDigestCacheArtifacts`, `TestVolumeImageDigestCacheApplicationChanges`, `TestVolumeImageDigestCacheEvictsChangedReferences`, `TestVolumeImageDigestCacheConcurrentInvalidation`: replace/remove mutable-tag inspection assumptions under the strict route; otherwise assert installed identities and existing user separation.
- **Conditional compatibility:** `TestManager`, `TestCollectOCITargetsCache`, `TestCollectOCITargetsErrorHandling`: optional store wiring and provider fixtures only.

#### `internal/agent/client/podman_test.go`

- **Add:** `TestPodmanContainerImageDigest`, `TestPodmanPreparedImageDigest`: immutable lookup, missing/invalid fields, actual manifest evidence and no mutable-tag substitution.
- **Conditional P3 add:** `TestPodmanStorageIdentity`.

#### `internal/agent/client/cri_test.go`

- Change `TestCRIImageRepoDigestsConfig`: correct configured image service and immutable-ID inspection.
- **Add:** `TestCRIImageManifestDigest`: valid manifest, opaque/config/index ambiguity and malformed local response behavior.
- **Conditional P3 add:** `TestCRIImageServiceIdentity`, `TestCRIContainerdImageEndpointSelection`, `TestCRIContainerdDestinationRouting`.

### Required documentation file

#### `docs/user/references/device-api-statuses.md`

- Change the existing **Application delta status** section, `lastDelta`/`imageDigests` table entries and outcome descriptions: current-digest lookup, delayed A/B transition, unknown/miss/eviction behavior, retained histories, completed-pull insertion and selected active dependency semantics.
- Remove the documented opaque-ID spec fallback and desired-spec-only retention/NotUsed-to-NotRequired cache upgrades.
- Keep OS behavior unchanged. Do not promise exact capacity 8 as a user contract.

### Conditional production, test and generated files

These are explicit alternatives, not authorization to implement both routes. If an unresolved route needs further files, inventory it before coding.

| File | Condition | Exact affected or proposed symbols and purpose |
|---|---|---|
| `internal/agent/agent.go` | P3/P4 shared current-context/lifecycle store wiring | `Agent.Run`: pass the existing concrete delta-status store to the application manager while preserving exporter order. |
| `internal/agent/device/applications/monitor.go` | P3/P4 requires context/completeness in monitor results | `AppStatusResult`, `monitor.Remove`, `monitor.updateWithWorkloads`: carry current metadata and avoid presenting queued removal as confirmed completion. |
| `internal/agent/device/applications/provider/provider.go` | P2 retained installed dependencies or target-role metadata | `ApplicationSpec`, `collectProviderTargets`, `decorateApplicationTargets`, `collectNestedForProvider`, `extractAppDataFromOCITarget`, `extractQuadletTargets`, `extractVolumeTargets`: distinguish installed/runtime role from desired collection without changing target/hint selection. Proposed add `ApplicationImageRole` only if metadata is needed. |
| `internal/agent/device/applications/provider/provider_test.go` | Above provider changes | `TestDecorateApplicationTargets`, `TestDecorateApplicationTargetsDoesNotTrackHelmChartAsWorkloadDelta`, `TestDecorateApplicationTargetsPreservesDesiredImages`, `TestCollectNestedForProvider_WhenHelmValuesChange_ItShouldInvalidateCache`; add `TestProviderInstalledIdentityDoesNotComeFromDesiredCache`. |
| `internal/agent/device/applications/provider/compose.go` | P2 retained installed parent/volume reporting | `composeProvider.Install`, `composeProvider.Spec`, `composeProvider.collectOCITargets`, `composeProvider.parentIsAvailable`: capture/label actual installed parent separately from desired extraction. |
| `internal/agent/device/applications/provider/compose_test.go` | Above | Add `TestComposeProviderInstalledParentIdentity`; conditionally adapt `TestComposeImageProvider`, `TestComposeInlineProvider`, `TestComposeEnsureDependencies` fixtures where changed installed identity/volume metadata reaches their exercised paths. |
| `internal/agent/device/applications/provider/quadlet.go` | P2 retained installed dependencies | `quadletProvider.Install`, `quadletProvider.Spec`, `quadletProvider.collectOCITargets`, `quadletProvider.parentIsAvailable`: installed identity and target role, separate from desired cache. |
| `internal/agent/device/applications/provider/quadlet_test.go` | Above | `TestInstallQuadlet`, `TestQuadletCollectOCITargetsWithKube`; add `TestQuadletProviderInstalledParentIdentity`. |
| `internal/agent/device/applications/provider/container.go` | P2 role labeling/volume identity needed | `containerProvider.Install`, `containerProvider.collectOCITargets`, `containerProvider.parentIsAvailable`: label real workload versus volume inputs; do not infer running identity from declared image. |
| `internal/agent/device/applications/provider/container_test.go` | Above | Add `TestContainerProviderWorkloadAndVolumeIdentityRoles`; conditionally adapt `TestContainerEnsureDependencies` only if changed concrete volume metadata affects its fixtures. |
| `internal/agent/device/applications/provider/helm.go` | P2 role metadata needed | `helmProvider.collectOCITargets`, `helmProvider.extractNestedTargets`: chart input remains separate from running workload identity; never invent a chart image digest. |
| `internal/agent/device/applications/provider/helm_test.go` | Above | Add `TestHelmProviderChartIsNotRuntimeImageEvidence`; conditionally adapt `TestHelmEnsureDependencies` and `TestHelmVerify_WhenRenderedLifecycleFieldsPresent_ItShouldNotRejectAsReadOnly` only if changed provider metadata affects their fixtures. |
| `internal/agent/device/applications/provider/volume.go` | P2 retained installed volume reporting | `Volume`, `volumeManager.Status`, `volumeManager.UpdateStatus`, `ToLifecycleVolumes`: carry verified installed source identity from materialization instead of mutable-tag inspection. |
| `internal/agent/device/applications/provider/volume_test.go` | Above | Add `TestVolumeStatusReportsInstalledImageIdentity`, `TestVolumeStatusUnknownImageIdentityRemainsUnknown`. |
| `internal/agent/device/applications/lifecycle/compose.go` | P2 volume materialization supplies identity | `Compose.ensurePodmanVolume`, `Compose.ensurePodmanVolumes`, `Compose.add`, `Compose.update`: capture immutable identity only after actual successful materialization. |
| `internal/agent/device/applications/lifecycle/compose_test.go` | Above | `TestComposeEnsurePodmanVolumeRetainReseeds`; add `TestComposeVolumeMaterializationCapturesInstalledIdentity`. |
| `internal/agent/device/applications/lifecycle/quadlet.go` | P2 quadlet volume materialization supplies identity | `Quadlet.ensureArtifactVolumes`, `Quadlet.add`, `Quadlet.Execute`: capture actual installed identity after successful actions. |
| `internal/agent/device/applications/lifecycle/quadlet_test.go` | Above | `TestQuadlet_ensureArtifactVolumes`, `TestQuadlet_Execute`; add `TestQuadletVolumeMaterializationCapturesInstalledIdentity`. |
| `internal/agent/device/applications/lifecycle/lifecycle.go` | P2 identity must cross lifecycle payload | `Volume`, `Action`: add verified installed immutable identity to the concrete payload only if the selected successful-materialization flow requires it. Settle how completed action evidence reaches the owning application under P2; do not invent identity in desired action inputs. |
| `internal/agent/client/ocidelta.go` | P3 importer source store must be configured | `OCIDelta`, `NewOCIDeltaFactory`, `Import`, `ApplyFromDirectory`: explicit accessible source-store/directory route; do not require intermediate destination equality. |
| `internal/agent/client/ocidelta_test.go` | Above | `TestOCIDeltaImport`, `TestOCIDeltaApply`; add `TestOCIDeltaApplySourceStorageRouting` and `TestOCIDeltaApplyFromDirectory` if that route changes. |
| `internal/agent/device/applications/mock_applications.go` | Only if `Application`/`Manager` interface changes prove necessary | Regenerate affected `MockApplication`/`MockManager` methods and recorder methods from provider-owned interface directives; do not hand-edit. Baseline plan preserves interfaces, so no change expected. |
| `internal/agent/device/applications/provider/mock_provider.go` | Only if P2 changes provider interfaces | Regenerate affected `MockProvider` methods/recorders; prefer concrete metadata and avoid interface changes. |
| `internal/agent/device/dependency/mock_dependency.go` | Only if a required dependency interface changes | Regenerate affected `MockPrefetchManager` methods/recorders. Baseline adds no public prefetch method, so no change expected. |

No OpenAPI/generated API change, dependency/module change, server-side change or integration-test file is expected in the core route. Do not use broad generation to create unrelated changes. Desired `provider/image_cache.go` remains a prefetch/extraction cache and should not become the reporting cache.

## Requirement-anchored unit-test strategy

| Test ID | Contract | Requirements | Primary tests |
|---|---|---|---|
| U1 | Old container retains A after mutable tag prepares B. | R1, R2 | Podman client/monitor tests. |
| U2 | Missing/opaque Kubernetes ID plus pinned spec stays unknown; running-state filtering is explicit. | R1 | Kubernetes/digest tests. |
| U3 | Desired parent B never appears as running A; volume semantics follow P2. | R1, R2 | Manager/application tests. |
| U4 | Source S, reconstructed X and final B stay distinct; key is B. | R2, R3, R9 | Podman/CRI prefetch tests. |
| U5 | Failed final pull inserts nothing; cached presence checks do not create results. | R3, R11 | Prefetch/store tests. |
| U6 | Insert A, insert B, status A, unknown, status B, restart, status B. | R4, R10 | Status/store sequence tests. |
| U7 | LRU hit/update order and capacity eviction; miss/unknown has no recency effect. | R5 | Store contract tests. |
| U8 | Equal digests in different application/target/runtime/user/store contexts select only their own results. | R4, R9 | Context isolation tests. |
| U9 | Rollout A/B both hit and aggregate; known-plus-unknown or miss suppresses aggregate; duplicates/order are deterministic. | R6 | Status/application aggregation tests. |
| U10 | Explicit invalid/unavailable/failed hint never discovers; missing hint full-pulls. | R7, R8 | Podman/CRI prefetch tests. |
| U11 | Intermediate store may differ; source access and final destination checks use the correct commands/context. | R9 | Client/CRI command fixtures. |
| U12 | Unrelated spec change retains entries; old active reference survives; failed removal does not retire; confirmed removal/recreation isolates histories. | R10 | Manager/store tests. |
| U13 | Unsafe version-1 results are discarded, OS state survives, order/capacity/duplicates and save failures are handled. | R10 | Store load/save tests. |
| U14 | Eviction, unknown/miss and status ticks do not change task pointer/readiness/queue or pull selection. | R11 | Public status/readiness contracts. |

Run focused changed-package unit tests during implementation, then applicable broader unit/lint checks if needed. Do not run `make integration-test` or any integration suite. Tests in this plan establish identity parsing, storage routing and failure contracts through fixtures; they do not establish actual containers/storage, CRI-O or containerd interoperability.

## Acceptance criteria

- P1-P5 are resolved or explicitly limit reporting to unknown; no implementation infers current/prepared digests from desired/source values.
- Each cached result is keyed by its verified final prepared digest in the selected application/target/destination context and follows completed-pull insertion rules.
- A while B is prepared selects A's cached result if available; later B selects B. No first-observation assignment or mismatch-driven deletion remains.
- Cache miss, eviction and unknown observations affect only reporting. No cache lookup drives registry resolution, delta selection or reconciliation.
- Status aggregates current observations and their lookup results; pending desired collection cannot hide a valid cached current result.
- Per-context retention is bounded by the chosen capacity; context count/lifetime has a reviewed bound without treating unknown observations as confirmed removal.
- Hint and no-hint behavior follows R7/R8; intermediate storage is flexible while final destination availability is verified by the implementation's local checks.
- Migration preserves OS state and refuses unsafe legacy application identity; restart restores valid histories and recency.
- Focused unit contracts pass and applicable lint is clean. No integration-test execution is required or authorized.
- Remaining real-runtime identity/storage compatibility limits are recorded for a separately authorized step, not presented as verified.

## Verification limits and next step

Official/runtime fixtures must settle exact manifest identity fields before coding the corresponding paths. Unit mocks can demonstrate intended user/socket/store routing but cannot prove real-runtime blob reuse, import interoperability or behavior on every supported runtime version. Report those limits explicitly; they do not authorize integration tests.

Review prerequisite decisions and select conditional routes, then update the inventory to remove unresolved rows or enumerate any newly discovered symbols. Implementation may begin only after that inventory and the source contracts are sufficiently concrete. Use `/revise` for decision changes and `/code` after the plan is accepted.

## Implemented verification boundaries

CRI `repoDigests` alone and opaque pull IDs do not prove a comparable selected manifest. Such preparations succeed normally but do not create application history entries. Qualified pull identity is accepted only with matching local runtime image evidence. CRI reporting requires an explicitly configured image-service endpoint; automatic endpoint probing remains unknown. CRI-O default apply-source storage cannot be proven from current source contracts, so explicit application hints use a completed full-pull fallback. Podman uses the runtime container's immutable image ID and the final pull's immutable local image ID. Unit fixtures verify command routing and parsing; actual CRI-O/containerd import interoperability is not claimed.
