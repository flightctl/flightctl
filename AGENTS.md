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

- **Build:** `make build` (requires Go ≥1.23, podman, and other deps; see [docs/developer/README.md](docs/developer/README.md)).
- **Generate API/client code and mocks:** `make generate` (requires mockgen: `go install go.uber.org/mock/mockgen@v0.4.0`).
- **Proto generation:** `make generate-proto` for `api/grpc/`.
- **Unit tests:** `make unit-test` (requires gotestsum: `go install gotest.tools/gotestsum@latest`). Avoid `make test`; prefer `make unit-test` (and `make integration-test` separately if needed). When verifying changes, first run unit tests on the specific files changed, then run `make unit-test` for the full suite.
- **Integration tests:** `make integration-test` (starts DB/KV/alertmanager via deploy targets).
- **E2E tests:** `make e2e-test` or `make in-cluster-e2e-test`; see [test/AGENTS.md](test/AGENTS.md).
- **Lint:** `make lint` (do not invoke golangci-lint directly; `make lint` installs and configures it automatically), `make lint-openapi`, `make lint-docs`, `make lint-helm`.
- **Code formatting:** Format Go imports with `gci write --skip-generated -s standard -s default .` (required for `make lint` to pass). Import order: standard library, then all other imports.
- **Cleanup:** `make clean` (containers and volumes), `make clean-all` (full cleanup including `bin/`).

## Running locally

- **Deploy to kind:** `make deploy` (creates cluster, builds containers, deploys Helm, prepares agent config). Optional: `DB_SIZE=small-1k` or `medium-10k` for larger DB.
- **Quadlets (systemd + Podman):** `make deploy-quadlets`. Certs and client config under `$HOME/.flightctl/`.
- **CLI:** After deploy, `bin/flightctl login <server> --web --certificate-authority ~/.flightctl/certs/ca.crt`, then `bin/flightctl apply -f examples/fleet.yaml`, `bin/flightctl get fleets`, etc.
- **Agent VM (Linux host):** `make agent-vm` / `make agent-vm-console`; see [docs/developer/README.md](docs/developer/README.md).

## Key conventions

- **Go:** Standard Go layout; avoid unnecessary dependencies; prefer existing patterns. Agent code follows strict rules—see [internal/agent/AGENTS.md](internal/agent/AGENTS.md).
- **API changes:** Edit OpenAPI YAML and hand-maintained types (e.g. `api/core/v1beta1/types.go`), then `make generate`. Do not edit `*.gen.go` by hand.
- **Documentation:** User docs under `docs/user/`, developer docs under `docs/developer/`. Run `make lint-docs` and `make spellcheck-docs` for user docs.

## Before committing

1. **Keep docs up to date** – If you change behavior, APIs, or workflows, update the relevant docs in `docs/user/` or `docs/developer/` and run `make lint-docs` (and `make spellcheck-docs` for user docs).
2. **Add test coverage** – New or changed code should include or extend unit tests (and integration tests where appropriate). Prefer table-driven tests and existing patterns; see [test/AGENTS.md](test/AGENTS.md) and [internal/agent/AGENTS.md](internal/agent/AGENTS.md) for agent code.
3. **Run lint** – Run `make lint` before committing and fix any issues.
4. **Run unit and integration tests** – Before committing, run `make unit-test` and `make integration-test` (integration tests require Podman; they start DB/KV/Alertmanager automatically). Fix any failures before pushing.

## Pointers to area-specific guidance

- **API (OpenAPI, codegen, versioning):** [api/AGENTS.md](api/AGENTS.md)
- **Device agent (reconciliation, lifecycle, testing):** [internal/agent/AGENTS.md](internal/agent/AGENTS.md)
- **Documentation (structure, style, lint):** [docs/AGENTS.md](docs/AGENTS.md)
- **Deployment (Helm, quadlets, kind):** [deploy/AGENTS.md](deploy/AGENTS.md)
- **Testing (unit, integration, e2e):** [test/AGENTS.md](test/AGENTS.md)

When making changes in a specific area, prefer the corresponding AGENTS.md and the linked docs over duplicating guidance here. For a human-oriented contribution workflow and PR expectations, see [CONTRIBUTING.md](CONTRIBUTING.md).
