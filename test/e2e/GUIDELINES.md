# E2E tests – concepts and guidelines

Concepts, architecture, and guidelines for writing and extending e2e tests, the harness, and aux (auxiliary) services. For how to run tests and which environment variables to set, see [README.md](README.md).

## Architecture

### Infra, harness, and aux

- **Infra** (`test/e2e/infra/`) is the single place for environment-specific behaviour. It abstracts Kubernetes vs Quadlet and provides:
  - Registry endpoint, RBAC, service config, secrets
  - K8s client and kubectl usage (only under `test/e2e/infra/k8s`; depguard enforces this)
  - Setup: `setup.EnsureDefaultProviders()`, `setup.GetDefaultProviders()`

- **Auxiliary** (`test/e2e/infra/auxiliary/`) provides shared testcontainer-based services that are the same for all deployments: registry, git server, Prometheus, and optionally tracing (Jaeger). Suites that need them call `auxiliary.Get(ctx)`; the package starts and reuses containers. It does not know about K8s or Quadlet.

- **Harness** (`test/harness/e2e/`) provides reusable helpers for e2e: devices, fleets, applications, agent, console, git, VM, etc. The harness may import infra, but **preferably it does not**; it should not hold registry/git state. Tests obtain data from infra (and from auxiliary) and pass it into harness methods as arguments.

### Data flow

1. Tests get **registry** from auxiliary: `auxiliary.Get(ctx).RegistryHost` and `.RegistryPort` → pass `(host, port)` into `GetDeviceImageRefForFleet`, `GetSleepAppImageRefForFleet`, and `CreateFleetDeviceSpec`.
2. Tests get **git/SSH** from auxiliary: `auxiliary.Get(ctx)` for host/port and internal URLs; `GetGitSSHPrivateKeyPath()` or `GetGitSSHPrivateKey()` for keys → pass into harness git methods and `CleanupGitRepositories`.
3. **RBAC, service config, cluster:** Use `setup.GetDefaultProviders().RBAC`, `.Infra` directly; do not route through the harness.

Do not duplicate infra logic in tests or harness. Do not use the raw Kubernetes client or `exec` kubectl outside `test/e2e/infra/k8s/`; add any such need to infra and expose it via interfaces.

## Directory layout

| Path | Purpose |
|------|---------|
| `test/e2e/<area>/` | One directory per area (e.g. `agent/`, `rollout/`, `resourcesync/`). Each has `*_suite_test.go` (entry, BeforeSuite/AfterSuite) and `*_test.go` (Ginkgo specs). |
| `test/harness/e2e/` | Shared harness code. Receives concrete inputs (registry host/port, git config, SSH paths) as method arguments. |
| `test/e2e/infra/` | Environment abstraction (K8s, Quadlet). `infra/setup`: providers. `infra/k8s`, `infra/quadlet`: deployment-specific impls. |
| `test/e2e/infra/auxiliary/` | Shared testcontainers: registry, git server, Prometheus, Jaeger. Package `auxiliary`; use `auxiliary.Get(ctx)` in BeforeSuite when the suite needs registry, git, or Prometheus. |
| `test/util/` | Common test utilities (e.g. constants, create helpers). |
| `test/scripts/` | Certs (`create_e2e_certs.sh`), agent images, VM creation, etc. |

## Writing a new test suite

1. **Create a directory** under `test/e2e/<area>/` (e.g. a new area or a new package under an existing area).

2. **Add `*_suite_test.go`:** Register the Ginkgo suite, and in `BeforeSuite`:
   - Call `auxiliary.Get(context.Background())` if the suite needs registry, git, or Prometheus. Store the result in a package-level variable (e.g. `auxSvcs *auxiliary.Services`) if tests need to read URLs or ports.
   - Pass registry host/port (and git config if needed) from the aux result into the harness and any deploy/scripts that need them.
   - Use `setup.GetDefaultProviders()` or `setup.EnsureDefaultProviders()` when you need infra (RBAC, service config, etc.).
   - Create the harness with the appropriate kubeconfig/env and pass it registry from auxiliary and git/infra data as needed.
   In `AfterSuite`, call `auxSvcs.Cleanup(ctx)` if you stored the services and are not reusing (reuse is the default for aux).

3. **Add `*_test.go`:** Write Ginkgo specs. Get registry host/port from auxiliary (e.g. package-level `auxSvcs`), git config and SSH key path from auxiliary or infra as needed, and pass them into harness methods. Preferably the harness does not import infra or auxiliary; keep it a library that receives inputs.

4. **Document requirements:** If the suite needs special setup (e.g. Quadlet VM, TPM, high RAM), add a `README.md` in the suite directory (see [quadlets/README.md](quadlets/README.md), [tpm/README.md](tpm/README.md)).

## Using the harness

- The harness **preferably** does not import `test/e2e/infra` or hold provider/registry/git state. Pass in:
  - **Registry:** `(registryHost, registryPort)` from `auxiliary.Get(ctx).RegistryHost` and `.RegistryPort`.
  - **Git:** Git server config and SSH key path or content from `auxiliary.Get(ctx)` (e.g. `GetGitSSHPrivateKeyPath()`, `GetGitSSHPrivateKey()`, and host/port fields).
- Add new reusable behaviour in `test/harness/e2e/` (e.g. new methods on `Harness`) when multiple suites will use it. Keep methods focused and take explicit parameters rather than reading from global infra.

## Auxiliary services

### What they are

The **auxiliary** package (`test/e2e/infra/auxiliary/`) provides:

- **Registry** – TLS registry on port 5000; used for agent and app images. Certificates come from e2e certs (`make prepare-e2e-test`).
- **Git server** – SSH git server (port 2222); generates SSH keys in the container; keys are copied to the host so tests and Repository CRs use the same key.
- **Prometheus** – Scrapes metrics; used by observability tests.
- **Tracing (Jaeger)** – Optional; started via `infra.TracingProvider` (e.g. `make start-tracing`) or by suites that use the tracing provider.

Containers are reused by default (`reuse=true`) so multiple suites can share them. Start/stop from the command line with `make start-aux`, `make stop-aux`, `make clean-aux`.

### How suites use them

- Call `auxiliary.Get(ctx)` in BeforeSuite (often with `context.Background()`). This starts all default aux services (registry, git, Prometheus) if not already running.
- Use the returned `*auxiliary.Services` for `RegistryHost`, `RegistryPort`, `GitServerHost`, `GitServerPort`, `GitServerInternalHost`, `GitServerInternalPort`, `PrometheusURL`, and for `GetGitSSHPrivateKeyPath()` / `GetGitSSHPrivateKey()`.
- Use the returned `*auxiliary.Services` (e.g. store in package-level `auxSvcs`) and pass its `RegistryHost`, `RegistryPort`, git fields, etc., into the harness and any scripts that need them.
- For tracing, use `infra.NewTracingProvider()` and its `StartTracing` / `StopTracing`; do not start Jaeger via the auxiliary package directly in suite code if you use the provider.

### Adding a new aux service

1. **Define the service** in `test/e2e/infra/auxiliary/`: add a `Service` constant and container name/lifecycle in the appropriate file (or a new one), and wire it into `StartServices` / `StopServices` and `serviceContainerNames`.
2. **Reuse and cleanup:** Use the same reuse/SkipReaper pattern as existing services so multiple suites can share the container. Register the container for cleanup where appropriate.
3. **Expose URLs/ports** on `auxiliary.Services` so suites and infra can pass them into the harness or config.
4. **CLI:** If the service should be startable/stoppable from the command line, add it to `cmd/aux-service` (e.g. new case in `parseServices` and in the usage string).
5. **Make:** Add `start-<service>` and `stop-<service>` targets in `test/test.mk` if needed, and document in [README.md](README.md).

## Conventions

- **No K8s client outside infra:** Do not use `k8s.io/client-go` or exec kubectl outside `test/e2e/infra/k8s/`. Implement cluster access in infra and expose it via provider interfaces.
- **Harness preferably has no infra:** The harness takes env-specific or runtime data as arguments (registry from auxiliary, git config, SSH paths). Tests get that data and pass it in.
- **Aux in BeforeSuite:** When a suite needs registry, git, or Prometheus, call `auxiliary.Get(ctx)` in BeforeSuite and store the result (e.g. in `auxSvcs`); pass host/port and config from it into the harness and scripts as needed.
- **Suite-specific READMEs:** Document special requirements (Quadlet VM, TPM, rollout multi-VM, etc.) in a `README.md` in the suite directory.
- **Ginkgo:** Use descriptive `Describe`/`Context`/`It` strings so `GINKGO_FOCUS` is useful. Use labels (e.g. `sanity`) where CI needs to filter.

## Quick reference

- **Run tests and env vars:** [README.md](README.md)
- **AI-oriented summary and layout:** [AGENTS.md](AGENTS.md)
- **Test strategy (unit, integration, e2e):** [../README.md](../README.md) and [../AGENTS.md](../AGENTS.md)
