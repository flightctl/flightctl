# Contributing to Flight Control

Thank you for your interest in contributing to Flight Control, a service for declarative management of fleets of edge devices and their workloads.

## Getting started

- **Developer setup and workflow:** See [docs/developer/README.md](docs/developer/README.md) for prerequisites, building, running locally (kind or Quadlets), and using the CLI and agent VM.
- **Project layout and conventions:** See [AGENTS.md](AGENTS.md) for repository structure, make targets, and pointers to area-specific guidance (API, agent, docs, deploy, tests). AGENTS.md is written for both humans and AI coding assistants.

## Before committing

1. **Keep docs up to date** – If you change behavior, APIs, or workflows, update the relevant docs in `docs/user/` or `docs/developer/` and run `make lint-docs` (and `make spellcheck-docs` for user docs).
2. **Add test coverage** – New or changed code should include or extend unit tests (and integration tests where appropriate). See [test/AGENTS.md](test/AGENTS.md) and, for agent code, [internal/agent/AGENTS.md](internal/agent/AGENTS.md).
3. **Run unit and integration tests** – Run `make unit-test` and `make integration-test` before committing (integration tests require Podman and start DB/KV/Alertmanager automatically). Fix any failures before pushing.
4. **Run relevant lint/checks** – When touching API or docs, run the checks described in [api/AGENTS.md](api/AGENTS.md) (e.g. `make generate`, `make lint-openapi`) and [docs/AGENTS.md](docs/AGENTS.md) (e.g. `make lint-docs`, `make spellcheck-docs`).

## Area-specific guidance

When working in a specific part of the codebase, follow the guidance in the corresponding file:

| Area | Guidance |
|------|----------|
| **API** (OpenAPI, types, codegen) | [api/AGENTS.md](api/AGENTS.md) |
| **Device agent** | [internal/agent/AGENTS.md](internal/agent/AGENTS.md) |
| **Documentation** | [docs/AGENTS.md](docs/AGENTS.md) |
| **Deployment** (Helm, quadlets) | [deploy/AGENTS.md](deploy/AGENTS.md) |
| **Tests** (unit, integration, e2e) | [test/AGENTS.md](test/AGENTS.md) |

## Commits

- **Signed commits** – All commits must be signed (e.g. with GPG or SSH). Configure signing in Git and sign each commit before pushing.
- **Jira prefix in title** – The first line of the commit message (the title) must be prefixed with the relevant Jira issue key, e.g. `EDM-1234: Short description of the change`. Use `NO-ISSUE:` for trivial or non-ticket changes (e.g. typos, minor docs). See the git history for examples: `git log --oneline`.

## Submitting changes

- Open a pull request against the main branch. Keep changes focused and minimal.
- Ensure CI passes (lint and tests). The “Before committing” steps above help avoid surprises.
- For larger or design-heavy changes, consider opening an issue or a Flight Control enhancement proposal (FEP) under `docs/developer/enhancements/` first.

## Questions

- **User and operator docs:** [docs/user/README.md](docs/user/README.md)
- **Developer and architecture docs:** [docs/developer/README.md](docs/developer/README.md) and [docs/developer/architecture/](docs/developer/architecture/)
