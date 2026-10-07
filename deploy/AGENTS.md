# Deployment – Guidelines for AI assistants

This directory contains everything needed to deploy the Flight Control service: Kubernetes/OpenShift (Helm), Podman (quadlets), and kind-based local dev. Use this file to avoid breaking deploy flows and to change the right artifacts.

## Layout

- **deploy/helm/** – Helm charts and e2e extras. Main chart: `deploy/helm/flightctl/` (values, templates, Chart.yaml). See [deploy/helm/flightctl/README.md](helm/flightctl/README.md). The catalog collector ships as a separate, independently installable chart: `deploy/helm/flightctl-catalog-collector/` – see [its README](helm/flightctl-catalog-collector/README.md).
- **deploy/podman/** – Quadlet unit files and config for running Flight Control as systemd-managed Podman containers (API, DB, worker, periodic, imagebuilder, observability, etc.). `flightctl-catalog-collector/` is the exception: it is an optional add-on, installed directly by the RPM instead of being rendered by `flightctl-standalone render quadlets`, and is not part of `flightctl.target`.
- **deploy/scripts/** – Shell scripts: `deploy_quadlets.sh`, `clean_quadlets.sh`, cert init, DB setup, migration.
- **deploy/kind.yaml** – kind cluster config (used by test/scripts and deploy).
- **Makefile integration:** Deployment targets are in `deploy/deploy.mk` and `deploy/agent-vm.mk`, included from the root Makefile.

## Main deployment paths

1. **Kind (local dev / e2e)**  
   - `make deploy` – Create kind cluster (if needed), build containers, deploy Helm, prepare agent config.  
   - Uses `test/scripts/install_kind.sh`, `test/scripts/create_cluster.sh`, `test/scripts/deploy_with_helm.sh`.  
   - Optional: `DB_SIZE=small-1k` or `medium-10k`; `SKIP_BUILD=1` to skip container builds.

2. **Quadlets (systemd + Podman)**  
   - `make deploy-quadlets` – Build containers (unless `SKIP_BUILD=1`), copy images to root podman, run `deploy/scripts/deploy_quadlets.sh`.  
   - Certs and client config end up in `$HOME/.flightctl/`.  
   - Cleanup: `make clean-quadlets` (runs `deploy/scripts/clean_quadlets.sh`).

3. **Database / KV only (for integration tests)**  
   - `make deploy-db` – DB via quadlet script.  
   - `make deploy-kv` – Key-value store.  
   - `make deploy-alertmanager` / `make deploy-alertmanager-proxy` – Alertmanager (optional).  
   - Integration tests use these before running tests (see `test/test.mk`).

4. **Redeploy single components (kind)**  
   - `make redeploy-api`, `make redeploy-worker`, `make redeploy-periodic`, etc. – Rebuild one container and redeploy via `test/scripts/redeploy.sh`.

## Helm specifics

- **Values:** `deploy/helm/flightctl/values.yaml` (base), `values.e2e.yaml`, `values.dev.yaml`, `values.nodeport.yaml`, etc. Lint uses `lint-values.yaml`.
- **Templates:** Go templates under `deploy/helm/flightctl/templates/` (API, UI, imagebuilder, certs, RBAC, etc.). Some filenames are generated (e.g. `README.md.gotmpl`, `Chart.yaml.gotmpl`).
- **Generated chart metadata:** `Chart.yaml` and `values.yaml` of both charts are rendered by `deploy/helm/cmd/charttmpl` from the `.gotmpl` sources beside them plus a build profile in `deploy/helm/helm-chart-opts.yaml` (`<edition>-<os>` for the main chart, `catalog-collector-<edition>-<os>` for the collector). That is how a downstream build rebrands the chart name, description, icon, annotations, and image registries. Edit the `.gotmpl` and the profile, then regenerate with `go generate ./deploy/helm/...`; do not hand-edit the generated files. Wiring a new chart in means adding its profiles and an entry to the `charts` list in `cmd/charttmpl/main.go`.
- **Lint:** `make lint-helm` runs `helm lint` for both charts with their lint values; the catalog collector chart is additionally linted against each of its example values files.
- **Render tests:** `make test-helm-catalog-collector` runs `deploy/helm/flightctl-catalog-collector/tests/render_test.sh`, which asserts the value combinations that must *fail* (no configuration, both configuration sources, `replicaCount > 1`, a `ServiceMonitor` without a `Service`) alongside the objects that must be produced. `helm lint` cannot express a must-fail case, so add new chart invariants there.
- **CI:** both targets run from `.github/workflows/lint-helm.yaml`, gated on changes to `deploy/helm/**`, the `Makefile`, or that workflow itself. A new lint or render target is only enforced once it is wired into that job.

## Podman / quadlets

- Each service has a `.container` (and often `.volume`, config dirs). Naming follows `flightctl-<service>`.
- **Ordering:** DB and KV start first; other services depend on them. The quadlet deploy script and targets enforce this.
- **Secrets:** Podman secrets are used for DB passwords, etc.; see `show-podman-secret` and script usage.

## What to edit

- **Helm chart (values, templates, Chart):** Under `deploy/helm/flightctl/`, or `deploy/helm/flightctl-catalog-collector/` for the catalog collector. After changes, run `make lint-helm` and ensure `make deploy` or `make deploy-helm` still works.
- **Quadlet units and config:** Under `deploy/podman/`. Keep ordering and env/config in sync with `deploy/scripts/deploy_quadlets.sh`.
- **Scripts:** `deploy/scripts/*.sh` – preserve idempotency and error handling; `make deploy-db`/`deploy-kv` depend on them. **`make integration-test`** uses testcontainers (`test/integration/preflight`) instead of quadlet `deploy-db`/`deploy-kv`/`deploy-alertmanager`.
