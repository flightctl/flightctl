# Catalog collector e2e suite

Initial-synchronization smoke test for the Flight Control catalog collector.

The suite starts a real, version-pinned [Kubeflow Model Registry](https://www.kubeflow.org/docs/components/hub/)
with its own PostgreSQL database, seeds one LIVE registered model with a SemVer
version and a digest-pinned ModelCar artifact, installs the shipped
`deploy/helm/flightctl-catalog-collector` chart into the cluster using the
collector image built from the branch under test, and then asserts through the
Flight Control API that the matching `Catalog` and `CatalogItem` appear with the
expected version, artifact reference, and collector ownership labels.

## Scope

In scope:

- First synchronization cycle from the Model Registry to the Flight Control API.
- Collector authentication against Flight Control with the OAuth2
  client-credentials grant, using the existing e2e Keycloak.
- Deployment through the shipped Helm chart, with credentials and the CA bundle
  mounted through chart values.

Out of scope (deliberately not covered here):

- Token renewal and re-authentication after expiry.
- Outage and recovery behaviour of either the registry or the API.
- The Quadlet (Podman) deployment of the collector.
- Anything involving a device, an agent, or a VM. The suite uses
  `SetupWorkerHarnessWithoutVM`.

## Requirements

- A Kubernetes deployment of Flight Control. The suite skips on Quadlet.
- A **kind** cluster. The collector image is a local build, and loading a local
  image into cluster nodes is only implemented for kind
  (`infra.ChartDeployer.LoadLocalImage`). The suite skips on OpenShift.
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
  nothing. The workflow also asserts, after the run, that the smoke spec
  actually executed rather than being skipped.

Skips for deployment types that cannot run the suite at all (Quadlet,
OpenShift) are unaffected by these variables.

## Environment variables

| Variable | Purpose |
|----------|---------|
| `E2E_CATALOG_COLLECTOR_IMAGE` | The exact collector image under test. Overrides the local-tag lookup. |
| `E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED` | `true` when that image is already in the cluster image store, so the suite neither looks for it in the host runtime nor loads it. |
| `E2E_CATALOG_COLLECTOR_REQUIRED` | `true` to fail setup instead of skipping when no collector image is available. Defaults to `true` whenever `E2E_CATALOG_COLLECTOR_IMAGE` is set. |
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

## What the suite sets up

1. **Aux services** — Keycloak and the Model Registry are started on demand
   through `auxiliary.StartServices`. Neither is in the default aux set, so
   suites that do not need them never pay for them.
2. **Identity** — the e2e Keycloak realm ships a dedicated
   `flightctl-catalog-collector` confidential client with a service account
   (client-credentials grant). The shared `e2e-keycloak` container is reused by
   name and only imports the realm file into an empty database, so the Keycloak
   fixture also reconciles that client, its audience mapper, and its service
   account through the Admin REST API on every start. A Keycloak left behind by
   an older run therefore converges instead of failing the grant with
   `invalid_client`; clients owned by other suites are left untouched. The
   suite registers an `AuthProvider` for the realm issuer so the Flight Control
   API accepts the collector's tokens.
3. **Seed data** — one LIVE registered model, one LIVE SemVer version, and one
   LIVE ModelCar artifact whose URI is pinned to an immutable `sha256:` digest.
4. **Deployment** — a per-run namespace holding the OAuth2 client credentials
   Secret and the Flight Control CA bundle ConfigMap, both mounted into the
   collector through `extraVolumes`/`extraVolumeMounts` chart values. TLS
   verification stays on: the collector verifies the API against that CA bundle
   and `insecureSkipVerify` is set nowhere.

Every name (namespace, Helm release, catalog, pipeline, model, artifact digest,
AuthProvider) carries the same random per-run suffix, so concurrent runs and
reruns against a reused cluster cannot collide.

## Teardown

`AfterSuite` runs the teardown as an ordered list of independent steps through
`infra.RunCleanup`. Every step runs even when an earlier one failed, and the
failures are reported together, so a Helm release that refuses to uninstall
cannot strand the catalog resources, the AuthProvider, and the namespace on a
cluster the next run reuses. A step that could not remove suite-owned state
fails the suite rather than logging a warning: the state it left behind is what
breaks the following run.

The order is:

1. **Stop the collector** — uninstall the Helm release, confirm it is gone, and
   wait for the pods to disappear. If any of that fails the namespace is
   deleted as a fallback, which stops the collector just as effectively; the
   original failure is still reported.
2. **Delete the catalog resources** the collector created. This is the one real
   ordering dependency: nothing may delete them until the collector can no
   longer recreate them, which step 1 guarantees.
3. **Archive the seeded Model Registry resources** (the Model Registry REST API
   has no delete, so they are moved out of the LIVE selection).
4. **Remove the suite-owned AuthProvider, namespace, and rendered values file.**
5. **Clean up the aux services** (a no-op while container reuse is on).

Each step is skipped when the setup step that created its resource never ran,
so a suite that skipped or failed early tears down only what it created.

On a failing spec, `AfterEach` dumps collector pod state, namespace events,
container logs, and the current catalogs and catalog items before teardown
removes them.
