# Flight Control - Claude Development Guide

## Common Bash Commands

- `make build` - Build the project
- `make unit-test` - Run unit tests (requires gotestsum)
- `make generate` - Generate API code and mocks (requires mockgen)
- `make deploy` - Deploy service locally in kind
- `make deploy DB_VERSION=small` - Deploy with small DB (1000 devices max)
- `make agent-vm` - Create agent VM for testing
- `make agent-container` - Create agent container for testing
- `make deploy-e2e-extras` - Start observability stack (Prometheus on :9090)
- `bin/devicesimulator --count=100` - Simulate 100 devices

## Core Files and Utility Functions

- `cmd/flightctl/main.go` - Main CLI entry point
- `cmd/flightctl-api/main.go` - API server entry point
- `cmd/flightctl-agent/main.go` - Agent entry point
- `internal/service/` - Core service logic
- `internal/agent/` - Agent implementation
- `api/v1alpha1/` - API definitions and types
- `docs/developer/` - Developer documentation
- `docs/user/` - User documentation

## Code Style Guidelines

- Use Go 1.23+ syntax and conventions
- Follow Kubernetes-like API patterns
- Use mTLS for agent communication, JWT for user authentication
- Implement proper error handling with `internal/flterrors/`
- Use structured logging with `pkg/log/`
- Write tests for new functionality
- Use mockgen for generating mocks

## Testing Instructions

- `make unit-test` - Run unit tests
- `make agent-vm` - Create test VM (user/user credentials)
- `make agent-container` - Create test container
- `bin/devicesimulator` - Simulate device load
- E2E tests in `test/e2e/` directory
- Integration tests in `test/integration/` directory

## Repository Etiquette

- Use descriptive branch names: `feature/device-enrollment`, `fix/agent-connection`
- Prefer rebase over merge for clean history
- Commit messages must start with JIRA issue key: `EDM-123: feat(agent): add device enrollment`
- PR names must start with JIRA issue key: `EDM-123: Add device enrollment feature`
- Update documentation for API changes
- Add tests for new functionality

## Developer Environment Setup

- Go 1.23+ required
- `kind` for local Kubernetes cluster
- `helm` v3.15+ for deployment
- `podman` with socket enabled: `systemctl --user enable --now podman.socket`
- `bubblewrap`, `openssl`, `openssl-devel` for building
- `go-rpm-macros` for RPM builds
- `gotestsum` for testing: `go install gotest.tools/gotestsum@latest`
- `mockgen` for mocks: `go install go.uber.org/mock/mockgen@v0.4.0`

## Unexpected Behaviors and Warnings

- Tests may fail if podman socket not enabled
- ARM64 builds need different PostgreSQL image in deployment
- Device simulator creates persistent processes - clean up manually
- Certificates and config files created in `$HOME/.flightctl/` during deployment

## Project Context

Flight Control is a GitOps-driven edge device fleet management service. It uses:
- ostree-based Linux images
- mTLS for secure agent communication
- PostgreSQL for state storage
- Kubernetes-like APIs
- Podman for container workloads
- TPM for hardware root-of-trust 