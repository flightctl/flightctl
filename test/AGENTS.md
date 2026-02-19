# Tests – Guidelines for AI assistants

This directory contains unit, integration, and e2e tests for Flight Control. Use this file to add or modify tests in the right layer and to run them correctly.

## Test layers

| Layer | Location | How to run | Notes |
|-------|----------|------------|--------|
| **Unit** | `internal/...`, `api/...`, `pkg/...` (Go `*_test.go`) | `make unit-test` | No external services. Uses `./internal/... ./api/... ./pkg/...`. |
| **Integration** | `test/integration/...` | `make integration-test` | Starts DB, KV, Alertmanager (via `make deploy-db deploy-kv deploy-alertmanager`), then runs tests. Use `TEST_DIR` or `TESTS` to narrow. |
| **E2E** | `test/e2e/...` | `make e2e-test` or `make in-cluster-e2e-test` | Full cluster (kind), agent images, qcow2; Ginkgo. Use `GO_E2E_DIRS` and `GINKGO_FOCUS` to filter. |

Defined in **test/test.mk** (included from root Makefile). Coverage reports go to `reports/` (e.g. `unit-coverage.out`, `integration-coverage.out`).

## Running tests

- **Unit:** `make unit-test` (optionally `VERBOSE=true`, or pass `TEST`).
- **Integration:** `make integration-test`. Requires Podman (DB/KV/Alertmanager). Optional: `TEST_DIR=./test/integration/...`, `TESTS=<regex>`, `FLIGHTCTL_TEST_DB_STRATEGY=local|template`.
- **E2E:**  
  - **Full flow (deploy + e2e):** `make e2e-test` – deploys cluster, builds e2e agent images, prepares qcow2, runs e2e.  
  - **Cluster already up:** `make in-cluster-e2e-test` – skips deploy, runs e2e.  
  - **Filter:** `GO_E2E_DIRS=test/e2e/agent`, `GINKGO_FOCUS="description"`, `GINKGO_PROCS=N`.  
  - Some e2e suites (e.g. quadlets) need a quadlet-capable VM; see `test/e2e/quadlets/README.md` and `test/e2e/tpm/README.md`. Rollout tests use multiple VMs and have higher RAM requirements; see `test/e2e/rollout/README.md`.

## E2E layout and harness

- **test/e2e/** – Ginkgo test packages (e.g. `agent/`, `applications/`, `resourcesync/`, `quadlets/`, `rbac/`). Each has `*_suite_test.go` and `*_test.go`.
- **test/harness/e2e/** – Shared helpers (harness_*.go) for e2e: devices, fleets, applications, agent, console, OC, git, VM, etc.
- **test/util/** – Common test utilities (e.g. Redis, constants, create helpers).
- **test/scripts/** – Environment setup, kind, certs (`create_e2e_certs.sh`), agent images, git-server, VM creation, redeploy. E2E certs/SSH under `bin/e2e-certs/`, `bin/.ssh/`.
- **test/integration/** – Integration test packages (store, service, tasks, imagebuilder_worker, etc.); no kind, use DB/KV from deploy targets.

## Conventions

- **Unit/integration:** Prefer table-driven tests; use `testify/require`; for agent code use gomock with `defer ctrl.Finish()`. See [internal/agent/AGENTS.md](../internal/agent/AGENTS.md) for agent-specific testing standards.
- **E2E:** Use the existing harness; add new helpers in `test/harness/e2e/` when reusable. Document suite-specific requirements in a `README.md` in the suite dir (e.g. quadlets, TPM).
- **Env vars:** Integration tests expect DB/KV passwords via env (set in test.mk). E2E may need `FLIGHTCTL_NS`, `KUBEADMIN_PASS`, etc.; export before `make` or set in CI.

## Prerequisites

- **Unit:** Go, gotestsum (`go install gotest.tools/gotestsum@latest`).
- **Integration:** Podman; `make deploy-db deploy-kv deploy-alertmanager` (or let `make integration-test` do it).
- **E2E:** kind, Podman, build tools, optional libvirt for agent VMs. `make e2e-test` / `make in-cluster-e2e-test` drive the rest (e2e-certs, agent images, qcow2 injection, etc.).

## Summary

1. **Unit** – `*_test.go` next to code; run with `make unit-test`.
2. **Integration** – Add under `test/integration/...`; run with `make integration-test` (DB/KV/Alertmanager started automatically).
3. **E2E** – Add under `test/e2e/<area>/`; use harness from `test/harness/e2e/`; run with `make e2e-test` or `make in-cluster-e2e-test`; use `GO_E2E_DIRS` and `GINKGO_FOCUS` to limit scope.
4. Document special requirements (e.g. quadlet VM, TPM) in a **README.md** in the relevant e2e suite directory.
