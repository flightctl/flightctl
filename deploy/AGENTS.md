# Deployment – Guidelines for AI assistants

This directory contains everything needed to deploy the Flight Control service: Kubernetes/OpenShift (Helm), Podman (quadlets), and kind-based local dev. Use this file to avoid breaking deploy flows and to change the right artifacts.

## Layout

- **deploy/helm/** – Helm chart and e2e extras. Main chart: `deploy/helm/flightctl/` (values, templates, Chart.yaml). See [deploy/helm/flightctl/README.md](helm/flightctl/README.md).
- **deploy/podman/** – Quadlet unit files and config for running Flight Control as systemd-managed Podman containers (API, DB, worker, periodic, imagebuilder, observability, etc.).
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
- **Lint:** `make lint-helm` runs `helm lint` with the chart’s lint values.

## Podman / quadlets

- Each service has a `.container` (and often `.volume`, config dirs). Naming follows `flightctl-<service>`.
- **Ordering:** DB and KV start first; other services depend on them. The quadlet deploy script and targets enforce this.
- **Secrets:** Podman secrets are used for DB passwords, etc.; see `show-podman-secret` and script usage.

## What to edit

- **Helm chart (values, templates, Chart):** Under `deploy/helm/flightctl/`. After changes, run `make lint-helm` and ensure `make deploy` or `make deploy-helm` still works.
- **Quadlet units and config:** Under `deploy/podman/`. Keep ordering and env/config in sync with `deploy/scripts/deploy_quadlets.sh`.
- **Scripts:** `deploy/scripts/*.sh` – preserve idempotency and error handling; integration tests and `make deploy-db`/`deploy-kv` depend on them.

## Summary

1. **Kind:** `make deploy` (and optional redeploy-*). Edit Helm under `deploy/helm/flightctl/` and run `make lint-helm`.
2. **Quadlets:** `make deploy-quadlets`; edit units and config under `deploy/podman/` and `deploy/scripts/`.
3. **DB/KV/Alertmanager:** Use `make deploy-db`, `deploy-kv`, etc.; do not change script contracts used by `test/test.mk` without updating tests.
4. New or changed Helm values/templates should be validated with `make lint-helm` and a test deploy.
