# Catalog collector e2e suite

Synchronization tests for the Flight Control catalog collector.

The suite starts a real, version-pinned [Kubeflow Model Registry](https://www.kubeflow.org/docs/components/hub/)
with its own PostgreSQL database, installs the shipped
`deploy/helm/flightctl-catalog-collector` chart into the cluster using the
collector image built from the branch under test, and then asserts through the
Flight Control API that registry content turns into the expected `Catalog` and
`CatalogItem` resources, and keeps matching them as that content changes.

## Scenarios

| Scenario | What it seeds | What it asserts |
|----------|---------------|-----------------|
| **Initial synchronization** | One LIVE model, SemVer version, digest-pinned ModelCar, seeded *before* the collector is installed | The catalog and catalog item appear with the model's metadata, the version-less repository, the immutable digest, and the collector ownership labels |
| **Update** | One model, then changed metadata plus a second eligible version on a different digest | The item is updated in place (same creation timestamp) and converges on the whole expected state — new description and provider, both version names, both digest references, one repository — and keeps the first version byte-for-byte |
| **Removal** | One model with two eligible versions | Archiving one version removes exactly that version and leaves the other unchanged; archiving the model prunes the catalog item while the configured catalog survives |
| **Failed-collection safety** | Two published models, then a selected version whose artifact URI is not a valid immutable reference, then a metadata change to one published model and an archive of the other | The collector is observed rejecting the collection both before and after those changes, and across several further poll intervals neither published item has been rewritten or pruned, the catalog survives, and nothing from the rejected content is published |

#### Why the update scenario waits for the whole expected state

The two registry changes it makes are two separate calls, and a collection
reads the model metadata and its versions separately as well. A cycle that
interleaves with them publishes both versions while still carrying the old
description, which is a legitimate intermediate state. Waiting only for the
version count and then asserting the metadata would reach those assertions
while it was showing, so every expected field is inside the bounded wait and
only the two invariants — the creation timestamp and the content of the
already-published version — are asserted on the state that satisfied it.

#### Why the failed-collection scenario changes content while the cycle fails

A spec that only leaves published content alone cannot tell "the collector
applied nothing" from "there was nothing to apply": a partial snapshot
containing an unchanged model reconciles to exactly the same result. So the
scenario makes the rejected snapshot differ from the published state in both
directions while the cycle is failing — one model's metadata is changed, and
the other model is archived — and then waits for rejected cycles *attempted
after* those changes before it starts watching. A partial update would show up
as the changed model's new description, and an unintended prune as the
archived model's item disappearing.

Freshness is established by counting: the rejection count is read after the
changes are made, and two further rejections are required. The first of those
may belong to a cycle that started before the changes; the second cannot,
because it can only start after the first was logged. So at least one whole
cycle saw the changed registry, tried to apply it, and was rejected.

The count is read once more after the observation window closes, and must have
grown. Otherwise the window proves less than it appears to: a source that
stopped cycling at all — a crashed pod, a loop that exited — would leave
published content untouched too, and every assertion in the window would pass
while nothing was being tested.

The retry cadence of a failing source is pinned flat in the rendered
configuration (`backoff.initialInterval` = `backoff.maxInterval`,
`multiplier: 1`, no jitter) for the same reason `pollInterval` is pinned: the
shipped default doubles from one second towards five minutes, so by the fourth
failure the next attempt would be further out than the whole observation
window and the wait would be bounded by the backoff rather than by anything
the collector did.

### Why each scenario is isolated

A collection cycle is all or nothing, and the Flight Control destination prunes
per pipeline. A single shared source would therefore let the failed-collection
scenario's deliberately unusable fixture stall every other scenario, and a
single shared pipeline would let one scenario prune another's resources.

So each scenario owns:

- a **custom property value** on the models it seeds
  (`flightctl_e2e_scenario=<scenario>-<run suffix>`), which its source filters
  on server-side, so no other scenario's content is ever fetched;
- a **source**, **pipeline**, and **catalog** of its own.

Each scenario also seeds its own registry content from its own spec (the
initial-synchronization one excepted, because it has to exist before the
collector starts), so no spec depends on another having run first and Ginkgo
may execute them in any order.

## Scope

In scope:

- Synchronization from the Model Registry to the Flight Control API: first
  cycle, updates, removals, and the safety of published content when a cycle
  fails.
- Collector authentication against Flight Control with the OAuth2
  client-credentials grant, using the existing e2e Keycloak.
- Deployment through the shipped Helm chart, with credentials and the CA bundle
  mounted through chart values. The suite drives this through
  `infra.CollectorDeployer` and never touches Helm, kubectl, or the Kubernetes
  client itself.

Out of scope (deliberately not covered here):

- Token renewal and re-authentication after expiry.
- Outage and recovery behaviour of either the registry or the API.
- The Quadlet (Podman) deployment of the collector.
- Anything involving a device, an agent, or a VM. The suite uses
  `SetupWorkerHarnessWithoutVM`.

## Requirements

- A Kubernetes deployment of Flight Control. On Quadlet the suite skips, unless
  the run required a collector (see below), in which case it fails.
- A **kind** cluster. The collector image is a local build, and loading a local
  image into cluster nodes is only implemented for kind. The suite does not
  check this itself: it asks `infra.CollectorDeployer.Available`, and the
  Kubernetes implementation reports the deployment as unavailable with that
  reason, so on OpenShift the suite skips — again, unless the run required a
  collector, in which case it fails.
- `helm` and `kind` on `PATH` (both are installed by the standard e2e
  environment setup).
- Podman (or Docker) for the Keycloak and Model Registry aux containers.
- The collector image built from the branch under test, i.e.
  `localhost/flightctl-catalog-collector-el9:latest` (or
  `localhost/flightctl-catalog-collector:latest`). `make deploy` builds it as
  part of `build-containers`; it can also be built on its own with
  `make flightctl-catalog-collector-container`. When no candidate image is
  present the suite **skips** with an explanatory message rather than silently
  testing some other build — unless the image was declared required, in which
  case setup **fails** (see below).

## Choosing the collector image

There are two paths, and they differ in where the image comes from:

- **Local development.** Nothing is set. The suite looks for the well-known
  local tags in the host container runtime and, when it finds one, loads it
  into kind itself. A container runtime that cannot answer the lookup is an
  error, not an "image not found": a broken runtime must not quietly turn the
  suite into a skip.
- **CI.** `.github/workflows/run-e2e-tests.yaml` passes the exact image from
  the build this run is testing. The job loads the backend image bundle
  straight into kind, so that image is never in the runner's host runtime;
  `E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED=true` tells the suite to use it as-is
  and skip both the host-runtime lookup and the image load.
  `E2E_CATALOG_COLLECTOR_REQUIRED=true` turns a missing image into a setup
  failure, because a silently skipped suite reports green while testing
  nothing. The workflow also asserts, after the run, that the initial
  synchronization spec actually executed rather than being skipped.

`E2E_CATALOG_COLLECTOR_REQUIRED` governs every reason a collector cannot be
deployed, not just a missing image. A deployment type that cannot host one at
all (Quadlet, OpenShift) is reported through the same
`infra.CollectorDeployer.Available` answer as an absent image, so one rule
decides what happens next. The run is either **optional** or **required**:

- **required** when `E2E_CATALOG_COLLECTOR_REQUIRED=true`.
- **required by default** on a Kubernetes deployment when
  `E2E_CATALOG_COLLECTOR_IMAGE` is set and `E2E_CATALOG_COLLECTOR_REQUIRED` is
  left unset — naming an exact image is itself a statement that this suite was
  meant to run against it. Quadlet does not apply that default: it cannot host
  a collector however the image was chosen, so it reads
  `E2E_CATALOG_COLLECTOR_REQUIRED` alone and an unset value leaves the run
  optional.
- **optional** when nothing is set, or whenever
  `E2E_CATALOG_COLLECTOR_REQUIRED` is explicitly `false`. An explicit `false`
  always wins, so an image can be named without making the run mandatory.

| Situation | Optional run | Required run |
|-----------|--------------|--------------|
| Deployment type cannot host a collector (Quadlet, OpenShift) | **skip**, with the reason | **fail** |
| No collector image available | **skip**, with the reason | **fail** |
| Setting is not a boolean, container runtime unreachable, or any other genuine error | **fail** | **fail** |

An error is never downgraded to a skip: only a settled "this environment
cannot run it" produces one, and only when the run is optional.

## Environment variables

| Variable | Purpose |
|----------|---------|
| `E2E_CATALOG_COLLECTOR_IMAGE` | The exact collector image under test. Overrides the local-tag lookup. |
| `E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED` | `true` when that image is already in the cluster image store, so the suite neither looks for it in the host runtime nor loads it. |
| `E2E_CATALOG_COLLECTOR_REQUIRED` | `true` to fail setup instead of skipping whenever a collector cannot be deployed — whether because no image is available or because the deployment type cannot host one. Defaults to `true` whenever `E2E_CATALOG_COLLECTOR_IMAGE` is set. |
| `KIND_CLUSTER_NAME` | kind cluster to load the collector image into. Defaults to `kind`. |

## Running

```bash
# Full environment (creates the kind cluster, builds all containers including
# the collector, deploys Flight Control) and then only this suite:
make deploy
make run-e2e-test GO_E2E_DIRS=./test/e2e/catalogcollector

# Against a cluster that is already up:
make flightctl-catalog-collector-container      # only if the image is missing
make run-e2e-test GO_E2E_DIRS=./test/e2e/catalogcollector
```

The aux services can also be driven by hand while debugging:

```bash
make start-keycloak
make start-model-registry      # Kubeflow Model Registry + PostgreSQL
make stop-model-registry
make stop-keycloak
```

The suite's own setup decisions are covered by plain Go tests that need no
cluster, including a check that the rendered collector configuration is
accepted by the collector's own parser and by the real source and destination
validators:

```bash
go test ./test/e2e/catalogcollector/ -run \
  'TestResolveCollectorImage|TestSuiteCleanupSteps|TestStopCollector|TestRenderCollectorConfig|TestNewTestResources|TestCountRejectedCollections'
```

(`TestCatalogCollector` is the Ginkgo entry point and does need a cluster, so
it is deliberately not matched by that pattern.)

## What the suite sets up

1. **Aux services** — Keycloak and the Model Registry are started on demand
   through `auxiliary.StartServices`. Neither is in the default aux set, so
   suites that do not need them never pay for them.
2. **Identity** — the e2e Keycloak realm ships a dedicated
   `flightctl-catalog-collector` confidential client with a service account
   (client-credentials grant). The shared `e2e-keycloak` container is reused by
   name and only imports the realm file into an empty database, so this suite
   reconciles that client, its audience mapper, and its service account through
   the Admin REST API itself, once the aux services are up and before the
   credentials are used. A Keycloak left behind by an older run — including one
   that predates this client — therefore converges instead of failing the grant
   with `invalid_client`; clients owned by other suites are left untouched.
   Because the client is this suite's alone, the reconciliation lives here and
   not in the shared Keycloak startup: other suites start the same container
   without it. The suite registers an `AuthProvider` for the realm issuer so
   the Flight Control API accepts the collector's tokens.
3. **Seed data** — the initial-synchronization model: one LIVE registered
   model, one LIVE SemVer version, and one LIVE ModelCar artifact whose URI is
   pinned to an immutable `sha256:` digest. Every other scenario seeds its own
   content once the collector is running.
4. **Deployment** — `infra.CollectorDeployer.Start`, given the rendered
   configuration, the OAuth2 client credentials, and a logical instance ID.
   Everything cluster-shaped behind that call — the per-run namespace, the
   credentials Secret, the Flight Control CA bundle ConfigMap, the
   `extraVolumes`/`extraVolumeMounts` chart values that mount both, the image
   load, and the release — belongs to `test/e2e/infra/k8s`, and the suite
   names none of it. What the suite does own is the configuration: one source
   and one pipeline per scenario. The endpoint and the in-container paths that
   configuration refers to come from `ConfigContext`, because only the
   deployment knows them. TLS verification stays on: the collector verifies
   the API against the CA bundle the deployment discovered and
   `insecureSkipVerify` is set nowhere.

Every name the suite owns (catalog, pipeline, scenario selector, model,
artifact digest, AuthProvider) carries the same random per-run suffix, which
is also the instance ID the deployment derives its own names from, so
concurrent runs and reruns against a reused cluster cannot collide.

## Teardown

`AfterSuite` runs the teardown as an ordered list of independent steps through
`infra.RunCleanup`. Every step runs even when an earlier one failed, and the
failures are reported together, so a Helm release that refuses to uninstall
cannot strand the catalog resources, the AuthProvider, and the namespace on a
cluster the next run reuses. A step that could not remove suite-owned state
fails the suite rather than logging a warning: the state it left behind is what
breaks the following run.

The order is:

1. **Stop the collector** — `infra.CollectorInstance.Stop`. How that is done,
   and what to fall back to when it does not work, belongs to the deployment:
   the Kubernetes implementation uninstalls the release, confirms it is gone,
   waits for the pods to disappear, and deletes the namespace as a fallback,
   which stops the collector just as effectively while still reporting the
   original failure. `Stop` answers two questions on two channels — whether
   the collector is confirmed down, and whether anything was left behind —
   because a fallback that worked makes those answers disagree.
2. **Delete the catalog resources** the collector created: the item of every
   model any scenario seeded, then every scenario's catalog. This is the one
   real ordering dependency: nothing may delete them until the collector can no
   longer recreate them. Step 1 records that separately from its own error,
   because a fallback that worked still stops the collector even though the
   step failed. When neither path could confirm the collector is down this
   step is skipped instead of attempted — deleting resources a live collector
   would immediately write back turns a reported failure into a silent one —
   and step 1's error names the catalogs it left behind.
3. **Archive the seeded Model Registry resources** — every model any scenario
   seeded, versions first (the Model Registry REST API has no delete, so they
   are moved out of the LIVE selection). A model is recorded as soon as the
   server accepts it, so content from a seeding call that failed halfway is
   retired too.
4. **Delete the suite-owned AuthProvider.**
5. **Remove the collector deployment** — `infra.CollectorInstance.Cleanup`,
   which removes whatever the deployment still owns (for Kubernetes, the
   namespace and the rendered values file). It is idempotent, so it is safe
   after a stop that already deleted part of it as its fallback, and it is
   independent of step 1, so it runs even when stopping failed.
6. **Clean up the aux services** (a no-op while container reuse is on).

Each step is skipped when the setup step that created its resource never ran,
so a suite that skipped or failed early tears down only what it created.

On a failing spec, `AfterEach` dumps `infra.CollectorInstance.Diagnostics`
(for Kubernetes: collector pod state, namespace events, and container logs)
and the current catalogs and catalog items before teardown removes them.
