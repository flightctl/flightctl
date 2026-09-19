# Internal implementation – Guidelines for AI assistants

This file covers server-side control-plane and component code under `internal/`.
Narrower guidance takes precedence, especially `agent/AGENTS.md` for device
agent code and component-specific files for Delta Worker and ImageBuilder.

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

## Package structure and ownership

- The existing control-plane service, store, and task roots are a legacy/shared
  layout: resource services live under `service/<resource>/`, stores under
  `store/<resource>/` (with shared persistence models under `store/model/`), and
  the older task families remain in the flat `tasks/` package. Preserve their
  locations when modifying existing code, but do not use them as the template
  for new component-specific code or migrate them opportunistically.
- Component-specific code puts the owning component prefix before the layer.
  New component code must not be added to a legacy top-level root when an
  owning component namespace exists. Preserve the component's established
  subpackage or flat-package shape unless a deliberate migration is in scope;
  follow the nearest component-specific `AGENTS.md` for its concrete layout.
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
- Task handlers orchestrate services and external work, not persistence.

## Interfaces and dependencies

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

## Constructor invariants

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

## Resource identity

Treat identity carried by the authoritative event or resource as the source of
truth unless a documented invariant requires a live lookup or recomputation.
For example, retain an event's organization and resource identifiers unless the
handler must validate current ownership or state.

## Persistence

Prefer database-enforced invariants, CAS, upsert, `RETURNING`, and set-based
operations over application-side read/loop/write sequences. Process collections
page by page when their size is not bounded by contract.

## Naming

Use names that describe the domain resource and operation, not vague states or
implementation-only details.
