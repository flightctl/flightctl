# Flight Control – Project context for AI assistants

Flight Control is a service for declarative management of fleets of edge devices and their workloads. This file orients AI tools (Cursor, Claude Code, etc.) to the repo layout, conventions, and where to find detailed guidance.

## Repository layout

| Path | Purpose |
|------|--------|
| **api/** | OpenAPI specs and generated types; versioned APIs (v1alpha1, v1beta1). See [api/AGENTS.md](api/AGENTS.md). |
| **cmd/** | Go entrypoints: `flightctl` (CLI), `flightctl-api`, `flightctl-agent`, `flightctl-worker`, `flightctl-periodic`, imagebuilder, PAM issuer, etc. |
| **internal/** | Service and agent implementation. `internal/agent/` has its own [internal/agent/AGENTS.md](internal/agent/AGENTS.md). |
| **pkg/** | Shared libraries (version, config, etc.). |
| **deploy/** | Deployment: Helm (Kubernetes/OpenShift) and Podman quadlets. See [deploy/AGENTS.md](deploy/AGENTS.md). |
| **test/** | Unit (`internal/`, `api/`), integration (`test/integration/`), e2e (`test/e2e/`). See [test/AGENTS.md](test/AGENTS.md). |
| **docs/** | User and developer documentation. See [docs/AGENTS.md](docs/AGENTS.md). |
| **hack/** | Scripts, Containerfiles for local/dev. |
| **packaging/** | RPM (packaging/rpm), Debian, systemd units, SELinux. |

## Build and development

- **Build:** `make build` (requires Go ≥1.26, podman, and other deps; see [docs/developer/README.md](docs/developer/README.md)).
- **Generate API/client code and mocks:** `make generate`; use the repository's `go:generate` directives, which invoke generators from the pinned Go module files.
- **Proto generation:** `make generate-proto` for `api/grpc/`.
- **Unit tests:** `make unit-test` (requires gotestsum: `go install gotest.tools/gotestsum@latest`). Avoid `make test`; prefer `make unit-test` (and `make integration-test` separately if needed). When verifying changes, first run unit tests on the specific files changed, then run `make unit-test` for the full suite. Two opt-out flags are available for faster local iteration: `RACE=0` disables the race detector and `COVERAGE=0` disables the coverage profile (e.g. `make unit-test RACE=0 COVERAGE=0`). Both default to `1` so CI always runs with race detection and coverage enabled.
- **Integration tests:** `make integration-test` (uses testcontainers for Postgres/Redis/Alertmanager; requires Podman). Key options: `INTEGRATION_PROCS=N` for parallelism, `TEST_DIR=./test/integration/store` for specific suites, `INTEGRATION_GINKGO_FOCUS="pattern"` for specific tests.
- **E2E tests:** `make e2e-test` or `make in-cluster-e2e-test`; see [test/AGENTS.md](test/AGENTS.md).
- **Lint:** `make lint` (do not invoke golangci-lint directly; `make lint` installs and configures it automatically), `make lint-openapi`, `make lint-docs`, `make lint-helm`, `make rpmlint`, `make lint-diagrams`.
- **Lint with auto-fix:** `make lint-fix` runs golangci-lint with `--fix`, writing fixes directly back to the working tree. Auto-fixes formatting (`gofmt`, `gci`), typos (`misspell`), and unnecessary type conversions (`unconvert`). Issues requiring human judgment (`gosec`, `staticcheck`, `govet`, `errcheck`, `depguard`, `unused`, `gocyclo`) are still reported but not auto-fixed.
- **Documentation checks:** `make spellcheck-docs` to check spelling, `make fix-spelling` for interactive fixing.
- **Code formatting:** Format Go imports with `gci write --skip-generated -s standard -s default .` (required for `make lint` to pass). Import order: standard library, then all other imports.
- **Dependency management:** `make tidy` to tidy go.mod files after adding/removing dependencies.
- **Cleanup:** `make clean` (containers and volumes), `make clean-all` (full cleanup including `bin/`).

## Running locally

- **Deploy to kind:** `make deploy` (creates cluster, builds containers, deploys Helm, prepares agent config). Optional: `DB_SIZE=small-1k` or `medium-10k` for larger DB.
- **Quadlets (systemd + Podman):** `make deploy-quadlets`. Certs and client config under `$HOME/.flightctl/`.
- **CLI:** After deploy, `bin/flightctl login <server> --web --certificate-authority ~/.flightctl/certs/ca.crt`, then `bin/flightctl apply -f examples/fleet.yaml`, `bin/flightctl get fleets`, etc.
- **Agent VM (Linux host):** `make agent-vm` / `make agent-vm-console`; see [docs/developer/README.md](docs/developer/README.md).

## Key conventions

- **Go:** Standard Go layout; avoid unnecessary dependencies; prefer existing patterns. Agent code follows strict rules—see [internal/agent/AGENTS.md](internal/agent/AGENTS.md).
- **Testing:** Use table-driven tests. Name test cases with "When ... it should ..." format for clarity.
- **API changes:** Edit OpenAPI YAML and hand-maintained types (e.g. `api/core/v1beta1/types.go`), then `make generate`. Do not edit `*.gen.go` by hand.
- **Documentation:** User docs under `docs/user/`, developer docs under `docs/developer/`. Run `make lint-docs` and `make spellcheck-docs` for user docs.
- **Commits:** All commits must be signed (GPG or SSH). Commit messages must be prefixed with Jira issue key (e.g., `<PROJECT>-<NUMBER>: Description`) or `NO-ISSUE:` for trivial changes.
- **Jira references:** Do not include Jira issue keys or Jira URLs in source code, comments, test names, or user-facing documentation. Track work in commit messages, pull requests, and Jira instead.

## Architecture and package layout

- Keep workflow control flow near its entrypoint. Services own business rules
  and events, stores own persistence and atomicity, and tasks orchestrate
  services and external work.
- Reuse existing mechanisms and preserve established layouts unless migration
  is explicitly in scope.

The repository uses two server-side patterns:

- **Legacy/shared:** `internal/service/<resource>/`,
  `internal/store/<resource>/`, shared models in `internal/store/model/`, and
  flat tasks in `internal/tasks/`.
- **Component-based:** `internal/<component>/{service,store,tasks}`; each
  component may use resource subpackages or an established aggregate/flat
  package.

| Runtime | Pattern and ownership |
|---------|-----------------------|
| `flightctl-api`, `flightctl-worker`, `flightctl-periodic` | Legacy/shared; workers use flat `internal/tasks/`. |
| `flightctl-alert-exporter`, `flightctl-alertmanager-proxy`, `flightctl-remote-access` | Consume legacy/shared services and stores. |
| `flightctl-delta-worker` | Owns component-based `internal/delta_worker/{service,store,tasks}/<resource>`; consumer and wiring stay at `tasks/`. |
| `flightctl-imagebuilder-api` | Owns aggregate component packages `internal/imagebuilder_api/{service,store}`. |
| `flightctl-imagebuilder-worker` | Owns flat `internal/imagebuilder_worker/tasks/` and consumes ImageBuilder/shared services and stores. |
| `flightctl-db-migrate`, `flightctl-restore` | Use shared stores; own no service or task packages. |
| `flightctl-agent` | Uses `internal/agent/`; follow [internal/agent/AGENTS.md](internal/agent/AGENTS.md). |

- Modify legacy/shared code in place; put new component-owned code under its
  component namespace. Component roots may consume shared services and wire
  stores, but stores must not depend on services, and tasks or unrelated
  services use service APIs instead of importing stores.
- Service packages following the interface/handler convention use `service.go`,
  `handler.go`, `docs.go`, adjacent tests, and generated `mock.go` and
  `traced.gen.go`; see [internal/service/AGENTS.md](internal/service/AGENTS.md).
- Put new task families under the owning component's `tasks/<task>/`; preserve
  established flat task packages unless migration is in scope.

### Mutation results

- When a database mutation determines the resulting state, return that state
  from the same atomic operation (for example, with `RETURNING`) and propagate
  it to callers. Do not immediately re-read the row merely to discover what the
  mutation wrote.
- Drive dependent events and other side effects from the returned mutation
  result instead of re-querying independently at each layer.
- Re-fetch only when the mutation cannot return the required data or when the
  operation intentionally requires a fresh, independent read.

### Interfaces and constructors

- Reuse an existing provider-owned interface. Create one only when required by
  the owning area, multiple production implementations, a system perimeter, or
  an explicit request; otherwise use the concrete service type.
- Do not add caller-side subsets or function fields solely for testing. Split a
  broad service into cohesive provider-owned services instead.
- New constructors validate required nil-able dependencies. Preserve existing
  constructor signatures unless their contract is deliberately being migrated.
- Required dependencies need no method-level nil checks; initialize optional
  dependencies with a no-op where practical and preserve documented optional
  behavior.

## Before committing

1. **Keep docs up to date** – If you change behavior, APIs, or workflows, update the relevant docs in `docs/user/` or `docs/developer/` and run `make lint-docs` (and `make spellcheck-docs` for user docs).
2. **Add test coverage** – New or changed code should include or extend unit tests (and integration tests where appropriate). Prefer table-driven tests and existing patterns; see [test/AGENTS.md](test/AGENTS.md) and [internal/agent/AGENTS.md](internal/agent/AGENTS.md) for agent code.
3. **Tidy dependencies** – Run `make tidy` after adding/removing dependencies or modifying go.mod files.
4. **Run lint** – Run `make lint` before committing and fix any issues. Use `make lint-fix` to auto-fix formatting, typos, and unnecessary conversions.
5. **Run unit and integration tests** – Before committing, run `make unit-test` and `make integration-test` (integration tests require Podman; they use testcontainers for Postgres/Redis/Alertmanager). Fix any failures before pushing.

## Pointers to area-specific guidance

- **API (OpenAPI, codegen, versioning):** [api/AGENTS.md](api/AGENTS.md)
- **Device agent (reconciliation, lifecycle, testing):** [internal/agent/AGENTS.md](internal/agent/AGENTS.md)
- **Service layer (mockgen/tracing-wrapper conventions for `internal/service/{resource}` sub-packages):** [internal/service/AGENTS.md](internal/service/AGENTS.md)
- **Documentation (structure, style, lint):** [docs/AGENTS.md](docs/AGENTS.md)
- **Deployment (Helm, quadlets, kind):** [deploy/AGENTS.md](deploy/AGENTS.md)
- **Testing (unit, integration, e2e):** [test/AGENTS.md](test/AGENTS.md)

When making changes in a specific area, prefer the corresponding AGENTS.md and the linked docs over duplicating guidance here. For a human-oriented contribution workflow and PR expectations, see [CONTRIBUTING.md](CONTRIBUTING.md).
