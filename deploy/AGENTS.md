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
   - The effective UID selects the runtime scope: regular users use rootless Podman kind and a delegated user cgroup scope; UID 0 uses the rootful provider. The worker needs KVM for rootless ImageExport; rootless ImageBuild runs Podman directly. Both worker paths still require runtime validation.

2. **Quadlets (systemd + Podman)**  
   - `make deploy-quadlets` – Build containers (unless `SKIP_BUILD=1`) in the invoking user's Podman store and run `deploy/scripts/deploy_quadlets.sh` in the matching systemd scope. It does not copy images to another Podman store.
   - Regular users use `systemctl --user`, XDG paths, and publish the gateway on host port 9443. UID 0 uses system systemd, system paths, and publishes the gateway on host port 443. Image-builder build paths use a separate generated drop-in. Rootless ImageExport needs KVM access; Quadlets preserve the user's supplementary groups, while Kind mounts `/dev/kvm` into the node. A named-user host ACL for the invoking UID may be needed for the nested worker when the KVM group is unmapped; host ACL, SELinux, and device-cgroup behavior still need runtime validation.
   - Cleanup: `make clean-quadlets` runs `deploy/scripts/clean_quadlets.sh` in the invoking UID's scope.
   - See [rootless deployment and local workflows](../docs/developer/rootless-development-plan.md) for host prerequisites, clean behavior, and remaining image-builder validation.

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
- **Scripts:** `deploy/scripts/*.sh` – preserve idempotency and error handling; `make deploy-db`/`deploy-kv` depend on them. **`make integration-test`** uses testcontainers (`test/integration/preflight`) instead of quadlet `deploy-db`/`deploy-kv`/`deploy-alertmanager`.
