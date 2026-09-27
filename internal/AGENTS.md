# Internal architecture and package layout

Use this runtime mapping when placing server-side code under `internal/`.

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
| `flightctl-agent` | Uses `internal/agent/`; follow [internal/agent/AGENTS.md](agent/AGENTS.md). |

- Modify legacy/shared code in place; put new component-owned code under its
  component namespace. Component roots may consume shared services and wire
  stores, but stores must not depend on services, and tasks or unrelated
  services use service APIs instead of importing stores.
- Service packages following the interface/handler convention use `service.go`,
  `handler.go`, `docs.go`, adjacent tests, and generated `mock.go` and
  `traced.gen.go`; see [internal/service/AGENTS.md](service/AGENTS.md).
- Put new task families under the owning component's `tasks/<task>/`; preserve
  established flat task packages unless migration is in scope.

## Mutation results

- When a database mutation determines the resulting state, return that state
  from the same atomic operation (for example, with `RETURNING`) and propagate
  it to callers. Do not immediately re-read the row merely to discover what the
  mutation wrote.
- Drive dependent events and other side effects from the returned mutation
  result instead of re-querying independently at each layer.
- Re-fetch only when the mutation cannot return the required data or when the
  operation intentionally requires a fresh, independent read.

## Interfaces and constructors

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
