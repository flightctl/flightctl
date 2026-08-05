# Tests – Guidelines for AI assistants

This directory contains unit, integration, and e2e tests for Flight Control. Use this file to add or modify tests in the right layer and to run them correctly.

## Test layers

| Layer | Location | How to run | Notes |
|-------|----------|------------|--------|
| **Unit** | `internal/...`, `api/...`, `pkg/...` (Go `*_test.go`) | `make unit-test` | No external services. Uses `./internal/... ./api/... ./pkg/...`. |
| **Integration** | `test/integration/...` | `make integration-test` | Starts Postgres, Redis, and Alertmanager via **testcontainers** (`go run ./test/integration/preflight` or the same logic from `test/integration/integrationstack` on first DB/harness use). `make run-integration-test` passes **`-tags=integration`** so `CreateTestDB` auto-starts the stack when needed; agent harness always calls `integrationstack.EnsureRunning`. Discovery uses **`podman port`** (see `test/util/testdb/integration_ports.go`). Use `TEST_DIR`, `TESTS`, `INTEGRATION_TEST_COUNT`, `INTEGRATION_GINKGO_FOCUS` to narrow or stress (see below). |
| **E2E** | `test/e2e/...` | `make e2e-test` or `make in-cluster-e2e-test` | Full cluster (kind), agent images, qcow2; Ginkgo. Use `GO_E2E_DIRS` and `GINKGO_FOCUS` to filter. |

Defined in **test/test.mk** (included from root Makefile). Coverage reports go to `reports/` (e.g. `unit-coverage.out`, `integration-coverage.out`).

## Running tests

- **Unit:** `make unit-test` (optionally `VERBOSE=true`, or pass `TEST`).
- **Integration:** `make integration-test`. Requires Podman (same socket model as e2e testcontainers; see `test/harness/containers`). Optional: `TEST_DIR=./test/integration/...`, `TESTS=<regex>` (passed to `go test -run`, usually the top-level `Test...` name), `FLIGHTCTL_TEST_DB_STRATEGY=local|template`, `INTEGRATION_TEST_COUNT=N` (default `1`; runs the integration test invocation **N** times with `go test -count=1` each time—required because **Ginkgo forbids `go test -count>1`**—stops on first failure), `INTEGRATION_GINKGO_FOCUS=<substring>` (Ginkgo `-ginkgo.focus` for suites that use Ginkgo under that `Test*`). **`make run-integration-test`** passes **`-tags=integration`**: the first `CreateTestDB` in each test process runs **`integrationstack.EnsureRunning`** (same as `go run ./test/integration/preflight start`) so containers exist before connecting. **`test/harness` (agent integration)** also calls **`EnsureRunning`** before creating the store, so agent suites start the stack even without the build tag. Raw **`go test ./test/integration/...`** without that tag skips auto-start in `CreateTestDB` (use **`go test -tags=integration ...`** or start **`make start-integration-services`** yourself). When **`flightctl-integration-postgres`** is running, `ApplyIntegrationConnectionOverrides` / Redis helpers use **`podman port`** (or `docker port` if `DOCKER_HOST` points at Docker); if Postgres is up but Redis or Alertmanager is missing when required, the run **fails fast**. If that Postgres container is **not** running, DB/KV stay at `NewDefault()` so unit-style callers of `testutil` still work.
- **E2E:**  
  - **Full flow (deploy + e2e):** `make e2e-test` – deploys cluster, builds e2e agent images, prepares qcow2, runs e2e.  
  - **Cluster already up:** `make in-cluster-e2e-test` – skips deploy, runs e2e.  
  - **Filter:** `GO_E2E_DIRS=test/e2e/agent`, `GINKGO_FOCUS="description"`, `GINKGO_PROCS=N`.  
  - Some e2e suites (e.g. quadlets) need a quadlet-capable VM; see `test/e2e/quadlets/README.md` and `test/e2e/tpm/README.md`. Rollout tests use multiple VMs and have higher RAM requirements; see `test/e2e/rollout/README.md`.

## E2E layout and harness

- **test/e2e/** – Ginkgo test packages (e.g. `agent/`, `applications/`, `resourcesync/`, `quadlets/`, `rbac/`). Each has `*_suite_test.go` and `*_test.go`.
- **test/harness/e2e/** – Shared helpers (harness_*.go) for e2e: devices, fleets, applications, agent, console, OC, git, VM, etc.
- **test/harness/containers/** – Shared testcontainers runtime (Podman socket, Ryuk) for integration preflight and e2e aux.
- **test/integration/preflight** – CLI wrapper around **`test/integration/integrationstack`** (`start|stop`). Host ports are **ephemeral** (runtime-assigned); tests read them with **`podman port`** / **`docker port`** on the fixed container names. **Make:** `make start-integration-services` / `make stop-integration-services`. `make integration-test` starts services, runs tests, then **always** runs `stop-integration-services` on `EXIT` (success or failure).
- **test/util/** – Common test utilities (e.g. Redis, constants, create helpers).
- **test/scripts/** – Environment setup, kind, certs (`create_e2e_certs.sh`), agent images, git-server, VM creation, redeploy. E2E certs/SSH under `bin/e2e-certs/`, `bin/.ssh/`.
- **test/integration/** – Integration test packages (store, service, tasks, imagebuilder_worker, etc.); no kind; DB/KV/Alertmanager endpoints are resolved from **`podman port`** when integration containers exist (`test/util/testdb/integration_ports.go`, `test/util/integration_net.go` for Redis helpers).

## Conventions

- **Unit/integration:** Prefer table-driven tests; use `testify/require`; for agent code use gomock with `defer ctrl.Finish()`. See [internal/agent/AGENTS.md](../internal/agent/AGENTS.md) for agent-specific testing standards.
- **E2E:** Use the existing harness; add new helpers in `test/harness/e2e/` when reusable. Document suite-specific requirements in a `README.md` in the suite dir (e.g. quadlets, TPM).
- **Kubectl / K8s client:** Do not use `exec` kubectl or the raw Kubernetes client outside `test/e2e/infra/k8s/`. Any such need must be implemented in infra (k8s, and quadlet with equivalent behaviour when relevant for both deployment types) and exposed via infra interfaces; see package infra.
- **Env vars:** Integration tests expect DB/KV passwords via env (set in test.mk). Endpoint discovery is automatic when integration containers are running (see `test/util/testdb/integration_ports.go`). E2E may need `FLIGHTCTL_NS`, `KUBEADMIN_PASS`, etc.; export before `make` or set in CI. For OCP with shared network (test VM and cluster on same network), optional `E2E_AUX_HOST` is the VM IP on the cluster network when the VM has multiple NICs (see test/README.md).

## Prerequisites

- **Unit:** Go, gotestsum (`go install gotest.tools/gotestsum@latest`).
- **Integration:** Podman (for testcontainers). Optional manual DB/KV for local debugging: `make deploy-db`, `deploy-kv`, etc. (standard ports); `make integration-test` does **not** use those targets.
- **E2E:** kind, Podman, build tools, optional libvirt for agent VMs. `make e2e-test` / `make in-cluster-e2e-test` drive the rest (e2e-certs, agent images, qcow2 injection, etc.).

## Summary

1. **Unit** – `*_test.go` next to code; run with `make unit-test`.
2. **Integration** – Add under `test/integration/...`; run with `make integration-test` (DB/KV/Alertmanager started automatically).
3. **E2E** – Add under `test/e2e/<area>/`; use harness from `test/harness/e2e/`; run with `make e2e-test` or `make in-cluster-e2e-test`; use `GO_E2E_DIRS` and `GINKGO_FOCUS` to limit scope.
4. Document special requirements (e.g. quadlet VM, TPM) in a **README.md** in the relevant e2e suite directory.
