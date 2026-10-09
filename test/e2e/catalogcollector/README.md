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
  `make flightctl-catalog-collector-container`. The suite **skips** with an
  explanatory message when no candidate image is present, rather than silently
  testing some other build.

## Environment variables

| Variable | Purpose |
|----------|---------|
| `E2E_CATALOG_COLLECTOR_IMAGE` | Overrides the collector image under test. Must already be loadable into the cluster. |
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
   (client-credentials grant). The suite registers an `AuthProvider` for the
   realm issuer so the Flight Control API accepts the collector's tokens.
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

`AfterSuite` asserts that teardown actually happened: the Helm release is
uninstalled and must no longer exist, the collector pods must disappear, and the
per-run namespace must be deleted. The catalog resources the collector created
are deleted through the API, and the seeded Model Registry resources are moved
to `ARCHIVED` (the Model Registry REST API has no delete).

On a failing spec, `AfterEach` dumps collector pod state, namespace events,
container logs, and the current catalogs and catalog items before teardown
removes them.
