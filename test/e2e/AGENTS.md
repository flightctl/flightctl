# E2E tests – context for AI assistants

## Philosophy

**Enforced by lint:** (1) `test/harness/e2e/` must not import `test/e2e/infra` or any subpackage (depguard `harness-no-infra`). (2) `k8s.io/client-go` may only be used under `test/e2e/infra/k8s` (depguard `test-no-k8s-client`); all other test code uses infra providers, not the K8s client directly.

- **Harness has no infra.** The harness (`test/harness/e2e/`) does not import `test/e2e/infra` or hold provider/registry/git state. Tests get data from infra and pass it in:
  - **Registry:** `setup.GetDefaultProviders().Infra.GetRegistryEndpoint()` → pass `(host, port)` into `GetDeviceImageRefForFleet`, `GetSleepAppImageRefForFleet`, `CreateFleetDeviceSpec`.
  - **Git / SSH:** `satellite.Get(ctx)` for git server; `testutil.GetSSHPrivateKeyPath()` (or similar) for SSH keys → pass into harness git methods and `CleanupGitRepositories`.
  - **RBAC, service config, cluster:** Use `setup.GetDefaultProviders().RBAC`, `.Infra` directly; do not route through the harness.
- **Infra is the single place for env-specific behaviour.** K8s client, kubectl, service config, RBAC, secrets live in `test/e2e/infra/` (and `infra/setup`). Tests and harness call infra; they do not duplicate its logic.

## Layout

- **test/e2e/<area>/** – One directory per area (e.g. `agent/`, `rollout/`, `resourcesync/`). Each has `*_suite_test.go` (entry + BeforeSuite/AfterSuite) and `*_test.go` (Ginkgo specs).
- **test/harness/e2e/** – Shared harness (devices, fleets, git, VM, etc.). Receives concrete inputs (registry, git config, SSH paths) as method arguments.
- **test/e2e/infra/** – Environment abstraction (K8s vs Quadlet); providers for registry, RBAC, service config, secrets. **test/e2e/infra/setup** – `EnsureDefaultProviders()`, `GetDefaultProviders()`.
- **test/e2e/infra/satellite** – Shared testcontainer services (registry, git server, prometheus). Same for K8s and Quadlet. Use `satellite.Get(ctx)` for git host/port and internal URLs (e.g. `GitServerHost`, `GitServerPort`, `GitServerInternalHost`, `GitServerInternalPort`). Call `SetEnvVars()` in BeforeSuite so deploy/infra can use them.

## Run

- `make run-e2e-test`
- Filter: `GO_E2E_DIRS=test/e2e/agent`, `GINKGO_FOCUS="description"`, `GINKGO_PROCS=N`
- More: [test/AGENTS.md](test/AGENTS.md)
