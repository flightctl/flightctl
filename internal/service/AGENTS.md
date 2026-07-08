# internal/service – Service layer codegen conventions

`internal/service` currently holds the monolithic `Service` interface
(`service.go`, ~140 methods across all resource types), its generated mock
(`mock_service.go`), and its hand-written tracing wrapper
(`traced_service.go`). This package is being decomposed into focused
`internal/service/{resource}/` sub-packages (Epic EDM-4668), each with its
own interface, handler, mock, and tracing wrapper. This file documents the
conventions those sub-packages must follow, established as part of
EDM-4675 (the sub-package migration itself, and creation of `service.go`
under each resource sub-package, happens in later stories).

## Mock generation convention

Each `internal/service/{resource}/` sub-package must carry its own
`docs.go` with a single per-interface `//go:generate mockgen` directive,
following the same shape already used across `internal/agent/**`
(e.g. `internal/agent/device/fileio/docs.go`):

```go
package {resource}

//go:generate go run -modfile=../../../tools/go.mod go.uber.org/mock/mockgen -source=service.go -destination=mock.go -package={resource}
```

- Use `go run -modfile=<path>/tools/go.mod go.uber.org/mock/mockgen ...`,
  **not** a bare `mockgen` binary invocation. This pins mockgen to the
  version declared in `tools/go.mod` (`go.uber.org/mock v0.4.0`), so
  `make generate` reproducibly regenerates mocks in CI without depending on
  a globally installed `mockgen`.
- Adjust the relative path to `tools/go.mod` for the sub-package's nesting
  depth. `internal/service/{resource}/` sits one level deeper than
  `internal/service/`, so the path is `../../../tools/go.mod` (three
  levels up), not the two-level `../../tools/go.mod` used by the removed
  monolithic directive.
- No `Makefile` change is required for new directives to take effect. The
  `generate` target runs `go generate -v $(go list ./... | grep -v -e api/grpc)`,
  which already discovers every `//go:generate` directive in the tree,
  including new ones added under `internal/service/{resource}/`.

The monolithic `//go:generate` directive that used to live in
`internal/service/docs.go` (regenerating `mock_service.go` for the full
`Service` interface) has been removed. `service.go` and `mock_service.go`
are left in place for now — they still compile and are still used by
existing tests — and will be deleted once all resource types have been
migrated to their own sub-packages.

## Tracing wrapper (`traced.go`) pattern

Each resource's tracing wrapper is **hand-written, not generated** —
mockgen has no equivalent for tracing wrappers, and this is a deliberate
choice (not a placeholder for a future generator). Follow the structural
pattern already used by the legacy `internal/service/traced_service.go`:

- A `Traced{Resource}` struct wraps the inner focused interface for that
  resource.
- A `WrapWithTracing(inner {Resource}Interface) {Resource}Interface`
  constructor returns `nil` unchanged and otherwise returns the wrapper.
- One method per interface method: start a span, delegate to the inner
  implementation, end the span, return the result unchanged. Do not add
  logic beyond tracing in this file.

### Span naming convention

Each method starts its span with:

```go
ctx, span := tracing.StartSpan(ctx, "flightctl/service/{resource}", "{Method}")
```

- `tracerName` (`tracing.StartSpan`'s first argument) is
  `"flightctl/service/{resource}"` (e.g. `"flightctl/service/device"`) —
  extending today's constant `"flightctl/service"` tracer name with a
  resource segment.
- `spanName` (the second argument) remains the bare method name (e.g.
  `"Get"`), which `tracing.StartSpan` kebab-cases internally.

Together these produce the identifier referenced by the design doc,
`flightctl/service/{resource}.{Method}` (e.g. `flightctl/service/device.Get`)
— the two segments map to `tracing.StartSpan`'s two existing parameters
rather than being concatenated into one combined, kebab-cased string
(which would mangle the intended dotted format).

> **Note:** this split-argument interpretation is the best reading of the
> design doc's naming example, but no `traced.go` exists yet to validate it
> against compiled code. The story that writes the first real `traced.go`
> should re-confirm this convention (e.g. by checking how the resulting
> span name renders in tracing output) before every subsequent resource
> copies it verbatim.

The first real `traced.go` created by a later story should carry a short
header comment pointing back to this document, so subsequent resources
copy the established pattern rather than reinventing it.

## Known migration coupling (for subsequent EDM-4668 stories)

`internal/service/teststore_framework_test.go` (the hand-written
`TestStore`/`Dummy*` fake-store framework used by `internal/service/*_test.go`)
has **no** coupling to `NewServiceHandler` — existing unit tests (e.g.
`internal/service/device_test.go`) construct `ServiceHandler{...}` struct
literals directly, bypassing the constructor entirely. Unit tests in this
package are therefore unaffected by constructor signature changes made
during the migration.

The real `NewServiceHandler`/`NewAuthProviderServiceHandler` constructor
coupling that later migration stories must account for lives in the real
wiring call sites and the integration test harness:

- `internal/api_server/server.go`
- `internal/api_server/agentserver/server.go`
- `internal/worker_server/server.go`
- `internal/periodic_checker/server.go`
- `cmd/flightctl-alert-exporter/main.go`
- `cmd/flightctl-alertmanager-proxy/main.go`
- `internal/imagebuilder_worker/worker.go`
- `internal/imagebuilder_api/server.go` (via `NewAuthProviderServiceHandler`)
- `internal/remote_access_server/server.go` (via `NewAuthProviderServiceHandler`)
- `test/harness/test_harness.go` (holds a `ServiceHandler service.Service`
  field, populated via `service.NewServiceHandler(...)`)
