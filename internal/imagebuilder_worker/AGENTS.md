# ImageBuilder worker – Guidelines for AI assistants

ImageBuilder worker task handlers use the established flat
`internal/imagebuilder_worker/tasks/` package. Preserve that shape unless a
deliberate migration is in scope; do not introduce a parallel
`tasks/<task>/` package layout for isolated changes.

## Task structure

- Keep operation-specific handlers and helpers in the flat `tasks/` package,
  with tests beside their implementations.
- Keep shared templates under `tasks/templates/`.
- Task handlers orchestrate existing service APIs and external work rather than
  importing stores for persistence.
- Keep worker construction and dependency wiring in the component's existing
  composition root.

## Validation

Run focused ImageBuilder worker tests first, then the applicable repository
unit and integration targets described in the root and `test/AGENTS.md`
guidance.
