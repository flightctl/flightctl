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

## Design and implementation structure

Keep architecture visible in package and file boundaries. New files and
packages should reflect domain responsibilities rather than grouping unrelated
helpers by implementation technique.

- Keep the primary workflow or handler control flow visible and close to its
  entrypoint.
- Where the code uses handlers, services, stores, or tasks, keep their roles
  distinct: orchestration coordinates work, services own business rules and
  resource events, and stores own persistence and atomic database operations.
- Do not move an entire workflow to another layer merely to separate files.
- Use existing project services, libraries, and neighboring implementations
  before introducing new wrappers or parallel mechanisms.

### Package structure and ownership

- For service packages that use the project's interface/handler/code-generation
  convention, keep the provider-owned interface in `service.go`, the concrete
  implementation and orchestration in `handler.go`, generation directives in
  `docs.go`, and adjacent tests in `*_test.go`. Keep generated mocks and
  tracing decorators in `mock.go` and `traced.gen.go`; do not hand-edit those
  generated files. Resource-specific helpers may remain in the same package
  when they support that service's workflow. The control-plane details and
  generator paths are documented in
  [internal/service/AGENTS.md](internal/service/AGENTS.md).
- The existing control-plane service, store, and task roots are a legacy/shared
  layout: resource services live under `internal/service/<resource>/`, stores
  under `internal/store/<resource>/` (with shared persistence models under
  `internal/store/model/`), and the older task families remain in the flat
  `internal/tasks/` package. These packages are used by `flightctl-api` and
  other existing control-plane binaries, so preserve their locations when
  modifying existing code and do not use them as the template for new
  component-specific code. Do not migrate them opportunistically as part of an
  unrelated change.
- Component-specific code puts the owning component prefix before the layer.
  The repository layouts are:
  - Delta Worker uses `internal/delta_worker/service/<resource>/`,
    `internal/delta_worker/store/<resource>/`, and
    `internal/delta_worker/tasks/<task>/`; its task consumer and wiring stay at
    `internal/delta_worker/tasks/`.
  - ImageBuilder API keeps its service and store packages at
    `internal/imagebuilder_api/service/` and
    `internal/imagebuilder_api/store/`; these are established aggregate
    packages with operation-specific files rather than one package per
    resource. ImageBuilder worker task handlers live in the established flat
    `internal/imagebuilder_worker/tasks/` package.
  New component code must follow this component-first rule and must not be
  added to a legacy top-level root when an owning component namespace exists.
  Preserve an existing component's established subpackage or flat-package
  shape unless a deliberate migration is in scope.
- In component-scoped code, keep persistence in the owning component's store
  namespace and keep the store API and concrete persistence implementation
  together with their tests. Stores own SQL, transactions, CAS, upserts, and
  other persistence invariants; services own business workflows and resource
  events. Composition roots may import stores to construct and wire services,
  but task and service logic should use the owning service API rather than
  reaching into an unrelated store.
- Keep the dependency direction one-way for new resource code: an owning
  service may depend on its store, but a store must not depend on services, and
  unrelated services or task packages must use the owning service API instead
  of importing that store. Coordinate cross-resource workflows through service
  APIs rather than adding new cross-store dependencies. Existing legacy and
  component-wiring exceptions are not a model for new code; do not extend them
  without an explicit architectural decision.
- For new task families, create a package under the owning component's
  `tasks/<task>/` directory. Keep a multi-step task workflow in `handler.go`
  (or use an explicit task-named file for a small event/completion adapter),
  task-specific helpers beside it, and tests next to the implementation. Keep
  queue consumption, dispatch, and wiring at the component's `tasks/` root;
  task handlers orchestrate services and external work, not persistence. For
  an existing flat task package such as `internal/tasks/` or
  `internal/imagebuilder_worker/tasks/`, preserve the flat layout unless a
  migration is explicitly in scope; do not add a second task-package pattern
  beside it.

### Interfaces and dependencies

Apply these rules in order:

1. Reuse an existing provider-owned service interface when one exists.
2. When the owning area's established convention requires a provider-owned
   interface, define it with the provider and keep its generated mock canonical
   across consumers.
3. Otherwise, default new internal dependencies to concrete service types.
   Introduce an interface only for multiple distinct production
   implementations, a system perimeter such as a third-party SDK, external
   HTTP client, or database driver, or an explicit user request.

Do not create caller-side or subset interfaces merely to restrict access to a
service or make unit tests mockable, and do not use function-valued dependency
fields as a substitute for a coherent service boundary. When a broad service
surface is the problem, decompose the concrete implementation into cohesive,
domain-focused services rather than hiding it behind caller-side facades.

### Constructor invariants

- For newly introduced constructors, validate nil-able required dependencies
  and return an error immediately when one is `nil`.
- When modifying an existing constructor, preserve its signature unless a
  deliberate constructor-contract migration is in scope. Do not cascade
  signature and call-site changes solely to add dependency validation.
- Do not add method-level defensive `nil` checks for required dependencies. Once
  construction succeeds, methods may rely on the established invariants.
- If a dependency is truly optional, initialize a no-op or Null Object in the
  constructor so normal methods do not need `nil` branches. Preserve intentional
  optional wrapper behavior documented by area-specific guidance.

```go
func NewService(repo Repository) (*Service, error) {
	if repo == nil {
		return nil, errors.New("repository is required")
	}
	return &Service{repo: repo}, nil
}

func (s *Service) Get(ctx context.Context, id string) (*User, error) {
	return s.repo.FindByID(ctx, id)
}
```

### Resource identity

Treat identity carried by the authoritative event or resource as the source of
truth unless a documented invariant requires a live lookup or recomputation.
For example, retain an event's organization and resource identifiers unless the
handler must validate current ownership or state.

### Persistence

Prefer database-enforced invariants, CAS, upsert, `RETURNING`, and set-based
operations over application-side read/loop/write sequences. Process collections
page by page when their size is not bounded by contract.

### Naming

Use names that describe the domain resource and operation, not vague states or
implementation-only details.

## Before committing

1. **Keep docs up to date** – If you change behavior, APIs, or workflows, update the relevant docs in `docs/user/` or `docs/developer/` and run the applicable documentation checks described above and in [docs/AGENTS.md](docs/AGENTS.md).
2. **Add test coverage** – New or changed code should include or extend unit tests (and integration tests where appropriate). Prefer table-driven tests and existing patterns; see [test/AGENTS.md](test/AGENTS.md) and [internal/agent/AGENTS.md](internal/agent/AGENTS.md) for area-specific standards.
3. **Tidy dependencies** – Run the dependency cleanup command after adding/removing dependencies or modifying Go module files.
4. **Run applicable lint checks** – Use the relevant project lint target for the files changed; see the command catalog above and area-specific guidance.
5. **Run applicable tests** – Run focused tests first, then the relevant full unit, integration, or E2E target; follow the environment requirements in [test/AGENTS.md](test/AGENTS.md).

## Pointers to area-specific guidance

- **API (OpenAPI, codegen, versioning):** [api/AGENTS.md](api/AGENTS.md)
- **Device agent (reconciliation, lifecycle, testing):** [internal/agent/AGENTS.md](internal/agent/AGENTS.md)
- **Service layer (mockgen/tracing-wrapper conventions for `internal/service/{resource}` sub-packages):** [internal/service/AGENTS.md](internal/service/AGENTS.md)
- **Documentation (structure, style, lint):** [docs/AGENTS.md](docs/AGENTS.md)
- **Deployment (Helm, quadlets, kind):** [deploy/AGENTS.md](deploy/AGENTS.md)
- **Testing (unit, integration, e2e):** [test/AGENTS.md](test/AGENTS.md)

When making changes in a specific area, prefer the corresponding AGENTS.md and the linked docs over duplicating guidance here. For a human-oriented contribution workflow and PR expectations, see [CONTRIBUTING.md](CONTRIBUTING.md).
