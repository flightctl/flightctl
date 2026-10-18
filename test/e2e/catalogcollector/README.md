# Catalog collector e2e suite

Synchronization tests for the Flight Control catalog collector.

The suite starts a real, version-pinned [Kubeflow Model Registry](https://www.kubeflow.org/docs/components/hub/)
with its own PostgreSQL, installs the shipped
`deploy/helm/flightctl-catalog-collector` chart using the collector image
built from the branch under test, and asserts through the Flight Control API
that registry content turns into the expected `Catalog` and `CatalogItem`
resources and keeps matching them as that content changes.

## Scenarios

Each scenario owns its own source, pipeline, catalog and seeded content, so
Ginkgo may run them in any order. Each carries one label. Why a scenario waits
the way it does, and why the retry cadence is pinned flat, is explained in
comments beside the code that depends on it.

| Label | Scenario | What it asserts |
|-------|----------|-----------------|
| `catalogcollector-smoke` | Initial synchronization | A LIVE model becomes a catalog and catalog item carrying the model's metadata, the version-less repository, the immutable digest and the collector ownership labels |
| `catalogcollector-update` | Update | Changed metadata and a second eligible version update the item in place — same creation timestamp — converging on the whole expected state while the already-published version stays byte-for-byte |
| `catalogcollector-removal` | Removal | Archiving a version removes exactly that version; archiving the model prunes the item while the configured catalog survives |
| `catalogcollector-failure-safety` | Failed-collection safety | With a version whose artifact URI is not a valid immutable reference, the collector is observed rejecting collections after further content changes, and nothing already published is rewritten or pruned |

The suite as a whole is labelled `catalogcollector`.

## Prerequisites

- A **kind** deployment of Flight Control. Loading a locally built image into
  cluster nodes is only implemented for kind, so on OpenShift or Quadlet the
  deployment reports itself unavailable and the suite skips — unless the run
  required a collector, in which case setup fails.
- `helm` and `kind` on `PATH` (both come with the standard e2e setup).
- Podman or Docker for the Keycloak and Model Registry aux containers. The
  suite starts both on demand; starting them yourself only prewarms them.
- The collector image. It is **not** part of `make build-containers`, so
  `make all`, `make deploy` and `make redeploy` do not produce it:

  ```bash
  make flightctl-catalog-collector-container
  ```

## Running

```bash
# Against a cluster that is already up:
make flightctl-catalog-collector-container
make run-e2e-test GO_E2E_DIRS=./test/e2e/catalogcollector

# From nothing (create the cluster and deploy Flight Control first):
make deploy
make flightctl-catalog-collector-container
make run-e2e-test GO_E2E_DIRS=./test/e2e/catalogcollector
```

Narrow to one scenario with `GINKGO_LABEL_FILTER=catalogcollector-smoke`.

## Environment variables

| Variable | Meaning |
|----------|---------|
| `E2E_CATALOG_COLLECTOR_IMAGE` | The exact collector image under test. Overrides the local-tag lookup, which tries `localhost/flightctl-catalog-collector-el9:latest` then `localhost/flightctl-catalog-collector:latest`. |
| `E2E_CATALOG_COLLECTOR_IMAGE_PRELOADED` | `true` when that image is already in the cluster's image store, so the suite neither looks for it in the host runtime nor loads it. |
| `E2E_CATALOG_COLLECTOR_REQUIRED` | `true` turns every reason a collector cannot be deployed — no image, or a deployment type that cannot host one — into a setup failure instead of a skip. Defaults to `true` whenever `E2E_CATALOG_COLLECTOR_IMAGE` is set. |

An unset variable takes the fallback above; a value that is not a boolean is
an error rather than a silent default. Which cluster the run talks to
(`KUBECONFIG`, `E2E_KUBE_CONTEXT`, `E2E_NAMESPACE`) is covered in the
[shared e2e README](../README.md).

## In CI

The suite is opt-in: it is not part of `sanity`, because it deploys a
component of its own and needs an image the default backend build does not
produce. Label the pull request to select it.

| Label | Effect |
|-------|--------|
| `run-e2e:catalogcollector` | The whole suite |
| `run-e2e:catalogcollector-smoke` | One scenario — any of the four labels above |
| `run-e2e:sanity+catalogcollector` | Sanity and the collector suite in one run |

A single flag derived from those labels both builds the collector image into
the backend bundle and exports the three variables above for the run, so an
ordinary `run-e2e` build produces no collector image and sets up no collector.
`test/ci` holds the regression tests for that wiring.

## Cleanup, debugging and fixture refresh

Teardown runs as an ordered list of independent steps whose failures are
reported together, so one stuck Helm release cannot strand the catalog
resources, the AuthProvider and the namespace on a cluster the next run
reuses. The one real ordering dependency is that catalog resources are deleted
only once the collector is confirmed stopped. On a failing spec, collector pod
state, namespace events and container logs are dumped before teardown.

To prewarm or inspect the aux services:

```bash
make start-keycloak
make start-model-registry
make stop-model-registry
make stop-keycloak
```

The Keycloak container is shared across runs and only imports
`flightctl-realm.json` when it starts with an empty database — and
`make clean-aux` does not remove it. If you change the realm file, or reuse a
Keycloak old enough to predate the collector client, run `make stop-keycloak`
once and let the next run import the realm afresh. A Keycloak that already has
the collector client needs nothing.
