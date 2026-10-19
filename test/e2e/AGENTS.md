# E2E tests – context for AI assistants

## Philosophy

**Enforced by lint:** `k8s.io/client-go` may only be used under `test/e2e/infra/k8s` (depguard `test-no-k8s-client`); all other test code uses infra providers, not the K8s client directly.

- **Harness has no infra.** The harness (`test/harness/e2e/`) does not import `test/e2e/infra` or hold provider/registry/git state. Tests get data and pass it in:
  - **Registry:** from auxiliary — `auxiliary.Get(ctx).Registry.Host` and `.Registry.Port` → pass `(host, port)` into `GetDeviceImageRefForFleet`, `GetSleepAppImageRefForFleet`, `CreateFleetDeviceSpec`.
  - **Git / SSH:** `auxiliary.Get(ctx)` for git server; `testutil.GetSSHPrivateKeyPath()` (or similar) for SSH keys → pass into harness git methods and `CleanupGitRepositories`.
  - **RBAC, service config, cluster:** Use `setup.GetDefaultProviders().RBAC`, `.Infra` directly; do not route through the harness.
- **Infra is the single place for env-specific behaviour.** K8s client, kubectl, service config, RBAC, secrets live in `test/e2e/infra/` (and `infra/setup`). Tests and harness call infra; they do not duplicate its logic.

## Layout

- **test/e2e/<area>/** – One directory per area (e.g. `agent/`, `rollout/`, `resourcesync/`). Each has `*_suite_test.go` (entry + BeforeSuite/AfterSuite) and `*_test.go` (Ginkgo specs).
- **test/harness/e2e/** – Shared harness (devices, fleets, git, VM, etc.). Receives concrete inputs (registry, git config, SSH paths) as method arguments.
- **test/e2e/infra/** – Environment abstraction (K8s vs Quadlet); providers for RBAC, service config, secrets (registry comes from auxiliary). **test/e2e/infra/setup** – `EnsureDefaultProviders()`, `GetDefaultProviders()`.
- **test/e2e/infra/auxiliary** – Shared testcontainer services (registry, git server, prometheus, plus on-demand Keycloak and Kubeflow Model Registry). Same for K8s and Quadlet. Use `auxiliary.Get(ctx)` in BeforeSuite when the suite needs registry, git, or Prometheus, and `auxiliary.StartServices(ctx, []auxiliary.Service{...})` for the on-demand ones; pass the returned host/port and config into the harness and scripts as needed.
- **Deploying a component under test:** suites that need an extra component deployed alongside Flight Control (e.g. the catalog collector) get a capability dedicated to that component on `setup.GetDefaultProviders()` — for the collector, `.CollectorDeployment` (`infra.CollectorDeployer`). The suite asks `Available` whether the component can be deployed here, `ConfigContext` for the endpoint and in-container paths it needs to render configuration, and `Start` for an `infra.CollectorInstance` exposing `WaitReady`, `Logs`, `Diagnostics`, `Stop` and `Cleanup`. Namespaces, Secrets, ConfigMaps, Helm values, image loading, releases, readiness and teardown all live in `test/e2e/infra/k8s`; the suite never names any of them, and never checks the deployment type itself. Add a capability per component rather than a general-purpose deployer, and always populate the provider slot: a deployment type that cannot host the component returns an unavailable result with a reason, not nil.

## Run

- `make run-e2e-test` (see [README.md](README.md) for all make targets and env vars)
- Filter: `GO_E2E_DIRS=test/e2e/agent`, `GINKGO_FOCUS="description"`, `GINKGO_PROCS=N`
- **Usage (env vars, running):** [README.md](README.md)
- **Concepts and guidelines (writing tests, harness, aux):** [GUIDELINES.md](GUIDELINES.md)
- More: [test/AGENTS.md](../AGENTS.md)
