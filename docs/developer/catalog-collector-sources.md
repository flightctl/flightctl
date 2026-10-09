# Writing custom catalog collector sources

The catalog collector is an extensible pipeline runner. This guide describes the component contracts in `pkg/catalogcollector` and how to add a source that is compiled into a collector distribution and emits catalog snapshots.

Read [Catalog collector overview](../user/using/catalog-collector/overview.md) first for the user-facing model, and the [configuration reference](../user/references/catalog-collector.md) for the fields of the shipped components.

## Choosing between an adapter and a compiled-in source

Neither path is needed when a shipped source already covers the external system. Check the [built-in sources](../user/using/catalog-collector/overview.md#built-in-sources) first. When none fits, decide which of the two integration paths to take before writing Go code.

| | HTTP adapter | Compiled-in source |
|---|---|---|
| Where the integration runs | Your own process, outside the collector | Inside the collector process |
| How snapshots are delivered | `POST` to the built-in `http` source | An in-memory call to the downstream consumer |
| What you implement | A converter from the external system to Catalog and CatalogItem JSON | A `Source` and a `SourceFactory` |
| What the collector gives you | Validation, processors, destinations, pruning | The same, plus optional helpers for polling, bounded backoff, preflight, and component metrics, and access to the shared authenticator extensions |
| Release coupling | None; ship on your own cadence | The source is compiled into a collector binary |

Prefer the adapter for a one-off or proprietary system. Prefer a compiled-in source for an API that several deployments share and that benefits from the collector lifecycle.

The `Source` contract does not require polling. A source only has to run until its context is cancelled and emit complete snapshots; the polling loop described below is a helper, not an obligation. The built-in `http` source is itself a source that never polls.

## Package layout

| Path | Contents |
|---|---|
| `pkg/catalogcollector` | Public contracts: `Source`, `Consumer`, `Destination`, `Extension`, `Host`, factories, `CatalogSnapshot`, `ComponentID`, `ValidateSnapshot` |
| `pkg/catalogcollector/config` | Configuration loader, strict decoding, environment expansion, the `Validator` interface |
| `pkg/catalogcollector/service` | Pipeline builder, supervisor, metrics runtime, instrumentation |
| `pkg/catalogcollector/source/...` | Shipped sources, and the reusable `pollsource` helper |
| `pkg/catalogcollector/processor/...` | Shipped processors |
| `pkg/catalogcollector/destination/...` | Shipped destinations |
| `pkg/catalogcollector/extension/...` | Shipped extensions, including the `extensionauth` capability interface |
| `cmd/flightctl-catalog-collector` | The official distribution: `main.go` and the `components()` composition point |

These packages are importable from outside the repository, so a custom distribution can reuse the framework without forking it.

## Component contracts

A source produces complete snapshots and hands them to the consumer its factory received:

```go
type Source interface {
    Run(ctx context.Context) error
}

type Consumer interface {
    Consume(ctx context.Context, snapshot *CatalogSnapshot) error
}
```

`Run` blocks until the context is cancelled or a fatal error occurs. The consumer the factory receives is always a fan-out, even when a single pipeline uses the source. Each branch of that fan-out leads to the first processor of its pipeline, or straight to the destination when the pipeline declares none. A source never resolves a destination itself.

Two optional capabilities are discovered by type assertion:

```go
// Validates external dependencies during startup, before readiness.
type SourcePreflight interface {
    Preflight(ctx context.Context) error
}

// Implemented by extensions that participate in the readiness lifecycle.
type Readiness interface {
    Ready()
    NotReady()
}
```

### Snapshot requirements

```go
type CatalogSnapshot struct {
    Revision     string
    Catalogs     []apiv1alpha1.Catalog
    CatalogItems []apiv1alpha1.CatalogItem
}
```

A source must honour the following rules:

* **Completeness.** Every emitted snapshot is the complete desired state for the configured scope. If any fetch, pagination step, or normalization step fails, return the error and emit nothing. Destinations prune on the strength of the snapshot, so a partial snapshot deletes live content.
* **Determinism.** `Revision` is opaque, and it changes only when the desired content changes. Derive it from the canonical content, for example a hash of the sorted resources. The `kubeflowmodelregistry` source sorts copies of both slices, marshals them as one canonical object, and uses the first 16 characters of the SHA-256 digest.
* **Replay is allowed.** A destination must not treat an unchanged revision as proof that the target is still correct, so re-emitting the same revision is a legitimate way to let destinations repair drift.
* **Immutability.** The snapshot and its slices belong to the caller. A consumer that retains or transforms data copies what it needs.
* **Stable ordering.** Sort output so that the revision does not depend on upstream pagination order.

The service validates every snapshot with `catalogcollector.ValidateSnapshot` before it reaches a destination: the revision must be non-empty, each resource must pass its API `Validate`, Catalog names must be unique, and each CatalogItem must have a unique catalog and name pair.

## Implementing a source

### 1. Define the configuration struct

The struct is decoded from the YAML body of the component entry. Use JSON tags, because decoding goes through `sigs.k8s.io/yaml`, and implement `config.Validator` so the collector rejects a bad configuration at startup rather than at the first poll:

```go
package examplesource

import (
    "encoding/json"
    "fmt"
    "strings"
    "time"

    "github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
)

// Duration accepts a Go duration string such as "5m" in the configuration
// file. The shipped components use an equivalent type from an internal
// package, which code outside this repository cannot import, so a custom
// distribution declares its own.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(data []byte) error {
    var text string
    if err := json.Unmarshal(data, &text); err != nil {
        return err
    }
    parsed, err := time.ParseDuration(text)
    if err != nil {
        return err
    }
    *d = Duration(parsed)
    return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
    return json.Marshal(time.Duration(d).String())
}

type AuthConfig struct {
    Authenticator string `json:"authenticator"`
}

type Config struct {
    Endpoint     string                   `json:"endpoint"`
    Catalog      string                   `json:"catalog"`
    PollInterval *Duration                `json:"pollInterval,omitempty"`
    Auth         *AuthConfig              `json:"auth,omitempty"`
    Backoff      pollsource.BackoffConfig `json:"backoff,omitempty"`
}

func (c *Config) pollIntervalOrDefault() time.Duration {
    if c.PollInterval == nil {
        return 5 * time.Minute
    }
    return time.Duration(*c.PollInterval)
}

func (c *Config) Validate() error {
    if strings.TrimSpace(c.Endpoint) == "" {
        return fmt.Errorf("missing required field %q", "endpoint")
    }
    if strings.TrimSpace(c.Catalog) == "" {
        return fmt.Errorf("missing required field %q", "catalog")
    }
    if c.PollInterval != nil && time.Duration(*c.PollInterval) <= 0 {
        return fmt.Errorf("field %q must be positive", "pollInterval")
    }
    if err := c.Backoff.Validate(); err != nil {
        return fmt.Errorf("backoff: %w", err)
    }
    return nil
}
```

> [!IMPORTANT]
> Import only `github.com/flightctl/flightctl/pkg/...` and `github.com/flightctl/flightctl/api/...` from a separate Go module. Go refuses to compile an external module that imports a path containing `internal`, so anything under `github.com/flightctl/flightctl/internal/...` is unavailable to a custom distribution. `pollsource.BackoffConfig` is safe to embed by value even though its fields have an internal type, because embedding it does not name that type.

Conventions that the shipped components follow:

* Use a pointer for an optional value whose zero value is meaningful, and resolve the default in an unexported accessor. A `*string` distinguishes "omitted, use the default" from "explicitly empty, disable this".
* Validate by naming the offending field, and never include a credential or a URI that may carry one in the error text.
* Sort map keys before validating them, so that an error is deterministic when several entries are invalid.
* Reject options that are mutually exclusive instead of silently preferring one.

### 2. Implement the factory

```go
const Type catalogcollector.ComponentType = "example"

type factory struct{}

func NewFactory() catalogcollector.SourceFactory { return &factory{} }

func (f *factory) Type() catalogcollector.ComponentType { return Type }

func (f *factory) CreateDefaultConfig() catalogcollector.ComponentConfig {
    return &Config{
        Backoff: pollsource.DefaultBackoffConfig(),
    }
}

func (f *factory) CreateSource(
    ctx context.Context,
    settings catalogcollector.Settings,
    cfg catalogcollector.ComponentConfig,
    next catalogcollector.Consumer,
) (catalogcollector.Source, error) {
    c, ok := cfg.(*Config)
    if !ok {
        return nil, fmt.Errorf("source %q: unexpected config type %T", settings.ID, cfg)
    }
    if err := c.Validate(); err != nil {
        return nil, fmt.Errorf("source %q: %w", settings.ID, err)
    }
    // ...
}
```

`Type()` is matched against the type part of the component identifier in the configuration file. `CreateDefaultConfig()` returns a pointer to a freshly populated struct; the framework decodes the user-supplied YAML on top of it, so every field absent from the file keeps its default.

`Settings` carries the per-instance context:

| Field | Use |
|---|---|
| `ID` | The parsed `type[/name]` identifier. Put it in error messages. |
| `Host` | Resolves configured extensions by identifier. Populated for every factory. |
| `Logger` | A `logrus.Entry` already scoped with `component_kind` and `component_id`. Never log raw configuration or credentials. |
| `MeterProvider` | Creates metric instruments. Never use the global meter provider; the service supplies either a dedicated Prometheus-backed provider or a no-op one. |

Re-validate in the factory even though the framework already called `Validate`. A custom distribution may construct components directly, and the assertion keeps the component self-contained.

### 3. Resolve extensions

An authenticator is an extension that implements a capability interface. Resolve it by identifier and assert the capability:

```go
import "github.com/flightctl/flightctl/pkg/catalogcollector/extension/extensionauth"

authID, err := catalogcollector.ParseComponentID(c.Auth.Authenticator)
if err != nil {
    return nil, fmt.Errorf("source %q: invalid auth.authenticator: %w", settings.ID, err)
}

ext, err := settings.Host.GetExtension(authID)
if err != nil {
    return nil, fmt.Errorf("source %q: resolving authenticator %q: %w", settings.ID, authID, err)
}

authenticator, ok := ext.(extensionauth.HTTPClient)
if !ok {
    return nil, fmt.Errorf(
        "source %q: extension %q does not implement extensionauth.HTTPClient",
        settings.ID, authID,
    )
}

transport, err := authenticator.RoundTripper(baseTransport)
```

`extensionauth.HTTPClient` wraps an `http.RoundTripper`, so the same `bearertokenauth` and `oauth2client` extensions that serve the shipped components serve yours. Resolve extensions during construction or `Start`, never in `CreateDefaultConfig`.

### 4. Reuse the polling helper

`pollsource.Helper` implements the loop that a pull-based source needs: an immediate first cycle, no overlapping cycles, `pollInterval` after a success, bounded exponential backoff with symmetric jitter after a failure, and cancellation that interrupts collection, downstream consumption, and waits.

Supply a `CollectFunc` that returns one complete snapshot or an error:

```go
type CollectFunc func(ctx context.Context) (*catalogcollector.CatalogSnapshot, error)
```

Install `Helper.OnCollect` to record collection metrics. The helper invokes it once per attempt, after collection finishes and before the snapshot reaches the consumer, so a downstream failure never changes the recorded collection outcome.

Return `nil` from `Run` when the error is the caller's own cancellation, so the service does not log an expected shutdown as a failure.

### 5. Add a preflight check

Implement `SourcePreflight` when the source depends on an external system. The service calls `Preflight` once during startup, after extensions start and before any source goroutine is launched, so an unreachable endpoint, a rejected credential, or an invalid filter fails before the collector reports ready.

Keep it cheap: one minimal query per dependency is enough.

### 6. Emit metrics

Create instruments from `Settings.MeterProvider`. Follow the conventions of the shipped components:

* Use dotted, lowercase, hierarchical instrument names under `flightctl.catalogcollector.`, and UCUM units.
* Record only bounded, low-cardinality attributes. Resource names, revisions, URLs, and error messages are never attribute values.
* Register an observable gauge callback with `meter.RegisterCallback` rather than `WithInt64Callback`, so that several instances of the same instrument name each keep their own callback.

## Registering the component

There is no global registry, no `init()` side effect, and no plugin loader. Factories are passed explicitly to `service.New` through `catalogcollector.Factories`:

```go
func components() catalogcollector.Factories {
    return catalogcollector.Factories{
        Sources: []catalogcollector.SourceFactory{
            httpsource.NewFactory(),
            kubeflowmodelregistrysource.NewFactory(),
        },
        Processors:   []catalogcollector.ProcessorFactory{ /* ... */ },
        Destinations: []catalogcollector.DestinationFactory{ /* ... */ },
        Extensions:   []catalogcollector.ExtensionFactory{ /* ... */ },
    }
}
```

To add a source inside this repository, add it to `cmd/flightctl-catalog-collector/components.go`. A factory with an empty `Type()`, or two factories that report the same `Type()` within one component kind, are rejected when the service builds its factory maps, so the mistake surfaces at startup rather than as a silently ignored component.

To ship a source from another repository, build your own distribution. Import `pkg/catalogcollector`, `pkg/catalogcollector/config`, and `pkg/catalogcollector/service`, provide a `components()` function that merges your factories with the built-in set, and call the same load-and-run sequence as `cmd/flightctl-catalog-collector/main.go`:

```go
cfg, err := config.Load(configPath)
if err != nil {
    return fmt.Errorf("loading configuration: %w", err)
}

svc, err := service.New(ctx, cfg, components(), service.Settings{Logger: logger})
if err != nil {
    return fmt.Errorf("building pipelines: %w", err)
}

return svc.Run(ctx)
```

## Configuration decoding and validation

Decoding happens in two passes, which is what lets provider-specific fields stay opaque until their factory is known:

1. `config.Parse` strict-decodes the file into sections, parses every component key as a `ComponentID`, stores each component body as raw JSON, and validates the pipeline references. Environment references are left untouched at this stage.
2. `config.DecodeComponent` resolves environment references in one component body, strict-decodes it on top of the struct from `CreateDefaultConfig`, and calls `Validate` when the struct implements `config.Validator`.

Consequences for a component author:

* Unknown fields are an error. A typo in a field name stops startup instead of being silently ignored.
* A body that is absent, `null`, or `{}` is normalized to no body at all. `DecodeComponent` still calls `Validate`, so a component with required fields reports them even when the entry is empty.
* Errors are prefixed with the configuration path, for example `sources.example/dev`. Do not repeat the path in your own error text.
* The expanded body is discarded after decoding, so resolved secrets are not retained in the generic configuration.

## Lifecycle

The service builds and runs components in a fixed order:

1. Every configured extension is constructed in deterministic order and registered in the host, whether or not a component references it. Extension factories must not resolve other extensions at this point.
2. Each pipeline branch is built backwards: the destination first, then the processors in reverse order.
3. Every referenced source is constructed once and receives one fan-out consumer holding all branches that reference it. A single-pipeline source still receives a one-branch fan-out.
4. `Service.Run` starts the metrics endpoint, then starts extensions, then runs source preflights, then launches source goroutines, and finally marks readiness extensions ready.
5. On shutdown, readiness is cleared first and extensions are shut down in reverse start order.

How many instances of each component kind exist follows from that order:

| Kind | Instances |
|---|---|
| Extension | One per declared identifier. Every declared extension is constructed and started. |
| Source | One per referenced source identifier, however many pipelines reference it. The single instance fans each snapshot out to every branch. |
| Processor | One per occurrence in a pipeline. Two pipelines that list the same processor identifier get two instances, because each is bound to its own branch's next consumer. |
| Destination | One per referenced destination identifier, shared by every branch that targets it. |

The sharing rules carry two obligations:

* A destination may be shared by several pipelines, so a destination implementation must be safe for concurrent use and must isolate ownership by the `pipelineID` it receives on each call.
* A source may feed several pipelines from one instance, so it must not assume a single downstream consumer and must not mutate a snapshot after handing it over. The fan-out passes the same snapshot pointer to every branch.

## Testing

Follow the repository testing conventions in [test/AGENTS.md](https://github.com/flightctl/flightctl/blob/main/test/AGENTS.md). For a new source, cover at least:

* **Configuration:** defaults from `CreateDefaultConfig`, strict rejection of unknown fields, every branch of `Validate`, and environment expansion where relevant.
* **Normalization:** the mapping from upstream objects to Catalog and CatalogItem resources, including name normalization, collisions, and rejected input.
* **Revision stability:** the same logical content produces the same revision regardless of upstream ordering, and different content produces a different revision.
* **Failure handling:** a failed fetch emits no snapshot, and cancellation returns without an error.
* **Snapshot validity:** feed produced snapshots through `catalogcollector.ValidateSnapshot`.

Use table-driven tests named in the "When ... it should ..." form, as used elsewhere in the repository.

Run the relevant checks before committing:

```console
make lint
```

```console
make unit-test
```

## Further reading

* [Catalog collector overview](../user/using/catalog-collector/overview.md)
* [Catalog collector configuration reference](../user/references/catalog-collector.md)
* [Installing the catalog collector](../user/installing/installing-catalog-collector.md)
