# ImageBuilder API – Guidelines for AI assistants

ImageBuilder API is component-scoped code. Keep its API, conversion, domain,
service, store, and transport code under `internal/imagebuilder_api/`; do not
add new ImageBuilder API code to legacy top-level service or store roots.

## Package layout

- `service/` is an established aggregate package with operation-specific files,
  not one package per resource.
- `store/` is an established aggregate package with operation-specific files,
  not one package per resource.
- Preserve these aggregate package shapes unless a deliberate migration is in
  scope.

## Boundaries

- Services own ImageBuilder business workflows and resource events; stores own
  SQL, transactions, and persistence invariants.
- Keep store interfaces, concrete implementations, and their tests in
  `store/`.
- Composition roots may import stores to construct services. Normal service
  logic should use the owning store API and service APIs rather than reaching
  into unrelated persistence packages.
- Reuse established shared control-plane service interfaces where the
  ImageBuilder API already depends on them.

## Validation

Run focused ImageBuilder API tests first, then the applicable repository unit
and integration targets described in the root and `test/AGENTS.md` guidance.
