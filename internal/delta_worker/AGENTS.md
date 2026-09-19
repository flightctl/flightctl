# Delta Worker – Guidelines for AI assistants

Delta Worker is component-scoped code. Keep its services, persistence, tasks,
and wiring under `internal/delta_worker/`; do not add new Delta Worker code to
the legacy top-level `internal/service/`, `internal/store/`, or
`internal/tasks/` roots.

## Package layout

- Resource services live under `service/<resource>/`.
- Resource stores live under `store/<resource>/`.
- Task families live under `tasks/<task>/`.
- Queue consumption, dispatch, and wiring remain at the `tasks/` root.
- Preserve these boundaries unless a deliberate migration is in scope.

## Services and stores

- Keep the provider-owned service interface in `service.go`, the concrete
  implementation and orchestration in `handler.go`, and generation directives
  in `docs.go`.
- Keep adjacent tests in `*_test.go`. Generated mocks and tracing decorators
  belong in `mock.go` and `traced.gen.go`; do not edit them by hand.
- Keep each store API, concrete persistence implementation, and tests together
  in the owning resource's store package.
- Services may depend on their owning stores. Stores must not depend on
  services, and unrelated services must use the owning service API instead of
  importing another resource's store.

## Tasks

- Keep a multi-step task workflow in `handler.go`; a small event or completion
  adapter may use an explicit task-named file.
- Keep task-specific helpers and tests beside the handler.
- Tasks orchestrate services and external work rather than accessing stores
  directly.

## Validation

Run focused Delta Worker tests first, then the applicable repository unit and
integration targets described in the root and `test/AGENTS.md` guidance.
