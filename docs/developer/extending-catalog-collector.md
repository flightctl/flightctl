# Extending the catalog collector

The catalog collector supports custom sources, processors, destinations, and extensions written in Go. Components are registered through factories and compiled into a collector distribution.

This guide describes the public contracts, configuration decoding, component lifecycle, and registration process. For configuration concepts, see [Catalog collector overview](../user/using/catalog-collector/overview.md). For shipped components and their settings, see the [configuration reference](../user/references/catalog-collector.md).

## Choosing an integration approach

Check the shipped components before implementing a custom integration. Choose the extension point according to the behavior you need:

| Approach | Use it to | Implementation |
|---|---|---|
| External adapter | Read another platform and submit complete snapshots without rebuilding the collector | A separate program that posts snapshot JSON to the built-in `http` source |
| Custom source | Obtain catalog content inside the collector process | `Source` and `SourceFactory` |
| Custom processor | Transform snapshots before they reach a destination | `Consumer` and `ProcessorFactory` |
| Custom destination | Write snapshots to another system or implement reconciliation behavior | `Destination` and `DestinationFactory` |
| Custom extension | Provide a shared capability, such as authentication or a health endpoint | `Extension`, `ExtensionFactory`, and any capability interfaces its clients need |

An external adapter can use any programming language and be released independently. A custom component participates in the collector lifecycle and requires a collector build that includes its factory.

Sources can poll an API, accept incoming requests, or use another delivery mechanism. The source contract does not require polling.

## Public packages

| Package or path | Purpose |
|---|---|
| `pkg/catalogcollector` | Component interfaces, factories, settings, identifiers, snapshots, and snapshot validation |
| `pkg/catalogcollector/config` | Configuration loading, strict decoding, environment expansion, and `Validator` |
| `pkg/catalogcollector/service` | Pipeline construction, source supervision, lifecycle management, and framework metrics |
| `pkg/catalogcollector/source/pollsource` | Reusable polling, retry backoff, and collection callbacks |
| `pkg/catalogcollector/extension/extensionauth` | The outgoing HTTP authentication capability |
| `pkg/catalogcollector/source`, `processor`, `destination`, and `extension` | Implementations shipped with the collector |
| `cmd/flightctl-catalog-collector` | The official entry point and factory composition |

A custom distribution can import the public `pkg/...` and `api/...` packages from another repository. Pin the Flight Control module version used by that distribution.

Do not import Flight Control's `internal/...` packages from an external integration repository. For example, shipped configurations use an internal duration type. An external component can define its own duration type or decode a string and validate it with `time.ParseDuration`.

`pollsource.BackoffConfig` can be embedded in your configuration without importing its internal field type. Initialize its defaults with `pollsource.DefaultBackoffConfig()`.

## Component contracts

The core interfaces are defined in `pkg/catalogcollector`:

```go
type Source interface {
    Run(ctx context.Context) error
}

type Consumer interface {
    Consume(ctx context.Context, snapshot *CatalogSnapshot) error
}

type Destination interface {
    Reconcile(
        ctx context.Context,
        pipelineID string,
        snapshot *CatalogSnapshot,
    ) error
}

type Extension interface {
    Start(ctx context.Context, host Host) error
    Shutdown(ctx context.Context) error
}

type Host interface {
    GetExtension(id ComponentID) (Extension, error)
}
```

The framework supplies the downstream consumer to source and processor factories. A source delivers snapshots through `next.Consume`; a processor transforms the input and forwards its output through the same method. The framework binds the pipeline identifier to the destination call.

### Instances and sharing

| Component | Construction and sharing |
|---|---|
| Source | One instance per referenced identifier, shared by every pipeline using that source |
| Processor | One instance per occurrence in a pipeline, bound to that occurrence's downstream consumer |
| Destination | One instance per referenced identifier, shared by every pipeline using that destination |
| Extension | One instance per declared identifier; every declared extension is constructed and started |

Unreferenced source, processor, and destination declarations are not constructed.

Each source receives a framework consumer that distributes snapshots to its pipelines. Delivery to those branches is synchronous and sequential. A branch failure does not prevent the remaining branches from being attempted, unless the context is cancelled. The returned error combines branch failures and identifies their pipelines.

Different sources run concurrently. A source that accepts concurrent submissions can also deliver snapshots concurrently. Destinations must be safe for concurrent use. Processors must protect mutable state if their source can call them concurrently. Shared extension capabilities must also support concurrent callers.

## Snapshot requirements

All source and processor implementations use the same snapshot type:

```go
type CatalogSnapshot struct {
    Revision     string
    Catalogs     []apiv1alpha1.Catalog
    CatalogItems []apiv1alpha1.CatalogItem
}
```

The resource types come from `github.com/flightctl/flightctl/api/core/v1alpha1`.

Follow these rules:

* **Complete desired state:** Emit the complete desired content for the source's configured scope. If collection, pagination, or mapping fails, emit no snapshot. With a destination that prunes, a partial snapshot can request deletion of omitted content.
* **Deterministic revision:** Use a non-empty identifier derived from the desired source content. Canonicalize resource ordering before hashing so pagination order does not change the revision.
* **Replay:** Allow the same revision to be emitted again. A destination must not assume that an unchanged revision means its target still matches the desired state.
* **Immutable input:** Consumers must not modify the supplied snapshot or its slices. Copy the snapshot, slices, maps, and pointer-backed values that you retain or change. A shallow struct or slice copy alone does not copy nested data.

An empty snapshot is valid when it has a non-empty revision. It represents an empty desired state. Its effect depends on the destination; the `flightctl` destination removes resources managed by that pipeline.

The framework calls `catalogcollector.ValidateSnapshot` after processors run, immediately before each destination. It rejects a nil snapshot, an empty revision, invalid resources, duplicate Catalog names, and duplicate CatalogItem catalog/name pairs.

Processors must check any input assumptions required by their transformations; resource validation has not necessarily run before their `Consume` method.

`Revision` identifies source content. The built-in `catalogname` processor preserves it while transforming resource names. Destinations must account for their actual target state and the transformed snapshot rather than relying only on the revision.

## Configuration and factories

Every component has a configuration struct and a factory. Use JSON field tags because component decoding uses `sigs.k8s.io/yaml`:

```go
type Config struct {
    Endpoint string `json:"endpoint"`
}
```

Implement `config.Validator` to check required fields, supported values, and relationships between options:

```go
type Validator interface {
    Validate() error
}
```

Return field-specific errors without including credentials or sensitive URLs. Use pointers when an explicit zero value must differ from an omitted field. Sort map keys when validation order affects which error is returned.

### Factory interfaces

All factories implement a common interface:

```go
type Factory interface {
    Type() ComponentType
    CreateDefaultConfig() ComponentConfig
}
```

`ComponentConfig` is an alias for `any`. `CreateDefaultConfig` must return a fresh pointer to the component's configuration struct. The decoder applies user fields to that value, preserving defaults for omitted fields.

The construction method depends on the component kind:

```go
type SourceFactory interface {
    Factory
    CreateSource(
        ctx context.Context,
        settings Settings,
        cfg ComponentConfig,
        next Consumer,
    ) (Source, error)
}

type ProcessorFactory interface {
    Factory
    CreateProcessor(
        ctx context.Context,
        settings Settings,
        cfg ComponentConfig,
        next Consumer,
    ) (Consumer, error)
}

type DestinationFactory interface {
    Factory
    CreateDestination(
        ctx context.Context,
        settings Settings,
        cfg ComponentConfig,
    ) (Destination, error)
}

type ExtensionFactory interface {
    Factory
    CreateExtension(
        ctx context.Context,
        settings Settings,
        cfg ComponentConfig,
    ) (Extension, error)
}
```

In the construction method, assert the expected configuration type and return an error for an unexpected type. The framework calls `Validate` during decoding when the configuration implements `config.Validator`. If your factory also supports direct callers, validate there as needed.

Do not start a source's collection loop in its factory. The service starts it through `Run`. An extension starts its managed resources through `Start`.

### Component settings

| Field | Purpose |
|---|---|
| `ID` | The configured `type[/name]` component identifier |
| `Host` | Access to configured extensions |
| `Logger` | A `logrus.Entry` scoped with `component_kind` and `component_id`; processor loggers also include `pipeline_id` |
| `MeterProvider` | The service's dedicated meter provider, or a no-op provider when metrics are disabled |

Use the supplied logger and meter provider. Do not log raw configuration, credentials, or HTTP authorization headers.

### Decoding and activation

Configuration is decoded in two stages:

1. `config.Parse` strictly checks the file structure, component identifiers, service settings, and pipeline references. Component bodies remain raw JSON.
2. During construction, `config.DecodeComponent` expands environment references, strictly decodes the body into the factory's default configuration, and invokes `Validate` when implemented.

Unused source, processor, and destination bodies are not decoded or expanded. Every declared extension body is decoded because every extension is constructed.

An absent, `null`, or empty-object body retains the factory defaults and still invokes `Validate`. Resolved environment values are stored in the typed configuration; the generic raw configuration is not replaced with an expanded copy.

For environment syntax and expansion boundaries, see [Environment variable references](../user/references/catalog-collector.md#environment-variable-references).

## Implementing sources

A source's `Run` method blocks until cancellation or a fatal error. It delivers complete snapshots to its downstream consumer and handles downstream errors according to its delivery mechanism. For example, a polling source can retry, while an HTTP source can return an error response to its caller.

Use the supplied context for collection and downstream calls. Return `nil` for expected caller cancellation. A non-nil error returned from `Run` causes the service to cancel the other sources and return an error.

### Polling helper

`pollsource.Helper` provides an immediate first collection, non-overlapping cycles, a polling interval after success, and bounded exponential backoff with jitter after failures.

Construct it with `pollsource.NewHelper`. Pass the component identifier, validated polling interval and backoff configuration, and component logger. Passing `nil` for the clock and jitter functions selects their production implementations.

The helper accepts a collection callback:

```go
type CollectFunc func(
    ctx context.Context,
) (*catalogcollector.CatalogSnapshot, error)
```

Return one complete snapshot on success. Return an error on failure; the helper does not forward a snapshot returned with an error. A nil snapshot with no error is treated as a collection failure.

A source using the helper can follow this pattern:

```go
type source struct {
    helper  *pollsource.Helper
    collect pollsource.CollectFunc
    next    catalogcollector.Consumer
}

func (s *source) Run(ctx context.Context) error {
    err := s.helper.Run(ctx, s.collect, s.next)
    if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
        return nil
    }
    return err
}
```

The helper resets backoff only when collection and downstream consumption both succeed. Each retry performs a new collection. Cancellation interrupts waits; collection and consumer implementations must honor their contexts.

Set `Helper.OnCollect` to observe collection attempts. The callback runs after collection and before downstream consumption, so its duration and error describe collection alone.

For a complete polling source, see [`kubeflowmodelregistrysource`](../../pkg/catalogcollector/source/kubeflowmodelregistrysource). For a source that accepts incoming requests, see [`httpsource`](../../pkg/catalogcollector/source/httpsource).

## Implementing processors

A processor implements `Consumer` and holds the `next` consumer supplied to `CreateProcessor`.

Transform a copy of the input, then return the result of `next.Consume(ctx, output)`. If transformation fails, return the error without forwarding an incomplete result. Preserve the context and propagate downstream errors.

Check that transformations preserve valid identities and references. For example, renaming a Catalog also requires updating matching CatalogItems' `metadata.catalog` values. Detect collisions introduced by the transformation.

The same input snapshot is delivered to other pipeline branches, so modifying it in place can change their content. Copy every nested value you modify.

For an implementation that copies changed data, checks collisions, and forwards the result, see [`catalognameprocessor`](../../pkg/catalogcollector/processor/catalognameprocessor).

## Implementing destinations

A destination implements `Destination`. Its `Reconcile` method receives the pipeline identifier and the validated snapshot after processing.

The framework does not impose an output protocol, ownership labels, pruning, transactions, or revision-based caching. Your destination defines those behaviors.

For a destination that manages remote resources:

* Keep management state separate for each `pipelineID`.
* Make reconciliation safe for concurrent calls. Serialize calls where required by the target or your internal state.
* Make repeated reconciliation safe, including retries after partial failures.
* Define which resources the destination may update or delete.
* Use the context for remote calls and bound request durations.
* Return failures to the caller rather than reporting success for incomplete work.

The framework does not roll back successful writes or retry destination calls itself. Retry behavior belongs to the source or submitting client.

For a reconciliation implementation, see [`flightctldestination`](../../pkg/catalogcollector/destination/flightctldestination). For a destination that logs snapshots without managing remote resources, see [`debugdestination`](../../pkg/catalogcollector/destination/debugdestination).

## Implementing extensions

An extension implements `Extension` and exposes any additional capability interfaces its clients need.

`Start` initializes the capability and returns; it must not block for the service's lifetime. `Shutdown` releases resources and stops background work. If `Start` fails after allocating resources, clean them up before returning: the service shuts down extensions whose `Start` succeeded.

All extension declarations are constructed before source, processor, and destination factories run. Those factories receive a host containing every extension, but the extensions have not started yet.

An extension factory must defer lookups of other extensions until `Start`, when the host is complete. Extension startup order is deterministic, but resolving another extension does not guarantee that its `Start` has already completed.

Sources, processors, and destinations do not have separate framework-managed `Start` and `Shutdown` methods. Use an extension for shared resources that require that lifecycle, or manage source-owned resources within `Run`.

### Resolving capabilities

Resolve an extension by its complete component identifier and check that it implements the required capability. Outgoing HTTP authentication uses:

```go
type HTTPClient interface {
    RoundTripper(base http.RoundTripper) (http.RoundTripper, error)
}
```

This interface is defined in `pkg/catalogcollector/extension/extensionauth`.

For example, a source or destination factory can use the following helper to wrap its configured transport:

```go
func authenticatedTransport(
    host catalogcollector.Host,
    id catalogcollector.ComponentID,
    base http.RoundTripper,
) (http.RoundTripper, error) {
    ext, err := host.GetExtension(id)
    if err != nil {
        return nil, err
    }

    auth, ok := ext.(extensionauth.HTTPClient)
    if !ok {
        return nil, fmt.Errorf(
            "extension %q does not implement extensionauth.HTTPClient", id,
        )
    }

    transport, err := auth.RoundTripper(base)
    if err != nil {
        return nil, err
    }
    if transport == nil {
        return nil, fmt.Errorf("extension %q returned a nil HTTP transport", id)
    }
    return transport, nil
}
```

Parse a configured string identifier with `catalogcollector.ParseComponentID` before calling the helper. Delay operations that require a started extension until preflight or runtime.

For capability and lifecycle examples, see [`bearertokenauthextension`](../../pkg/catalogcollector/extension/bearertokenauthextension) and [`healthcheckextension`](../../pkg/catalogcollector/extension/healthcheckextension).

## Startup checks and readiness

Sources and destinations can implement optional startup checks:

```go
type SourcePreflight interface {
    Preflight(ctx context.Context) error
}

type DestinationPreflight interface {
    Preflight(ctx context.Context) error
}
```

The service discovers these interfaces by type assertion. After extensions start, it runs destination preflights followed by source preflights, once per constructed instance. A failure stops startup before source goroutines are launched.

Use bounded checks with the same clients, authentication, and transport settings as runtime operations. State what the checks verify. A read-only list request, for example, does not prove write or delete permissions. Startup checks do not replace runtime error handling.

An extension can participate in readiness through:

```go
type Readiness interface {
    Ready()
    NotReady()
}
```

Readiness is marked after startup checks pass and every source goroutine enters its run wrapper. It does not wait for the first successful collection or synchronization.

## Registering components and building a distribution

Factories are supplied explicitly to `service.New` through `catalogcollector.Factories`. Components are selected from those factories by the type part of their configured identifier.

For example, the following composition includes shipped components and hypothetical custom packages imported as `mysource`, `myprocessor`, `mydestination`, and `myextension`:

```go
func components() catalogcollector.Factories {
    return catalogcollector.Factories{
        Sources: []catalogcollector.SourceFactory{
            httpsource.NewFactory(),
            kubeflowmodelregistrysource.NewFactory(),
            mysource.NewFactory(),
        },
        Processors: []catalogcollector.ProcessorFactory{
            catalognameprocessor.NewFactory(),
            myprocessor.NewFactory(),
        },
        Destinations: []catalogcollector.DestinationFactory{
            debugdestination.NewFactory(),
            flightctldestination.NewFactory(),
            mydestination.NewFactory(),
        },
        Extensions: []catalogcollector.ExtensionFactory{
            healthcheckextension.NewFactory(),
            bearertokenauthextension.NewFactory(),
            oauth2clientauthextension.NewFactory(),
            myextension.NewFactory(),
        },
    }
}
```

Include the factories needed by your distribution. An empty factory type or duplicate types within one component kind are rejected by `service.New`. The same type can be registered for different component kinds.

To add a component to the official collector, register its factory in [`cmd/flightctl-catalog-collector/components.go`](../../cmd/flightctl-catalog-collector/components.go).

To ship components from another repository, create a custom entry point that imports the framework and your component packages. Supply your factory set and use the same load-and-run sequence:

```go
func run(
    ctx context.Context,
    configPath string,
    factories catalogcollector.Factories,
    logger *logrus.Logger,
) error {
    cfg, err := config.Load(configPath)
    if err != nil {
        return fmt.Errorf("loading configuration: %w", err)
    }

    svc, err := service.New(
        ctx, cfg, factories, service.Settings{Logger: logger},
    )
    if err != nil {
        return fmt.Errorf("building pipelines: %w", err)
    }
    return svc.Run(ctx)
}
```

Use `pkg/catalogcollector/config`, `pkg/catalogcollector/service`, and a non-nil logger. Arrange cancellation in your entry point, for example with `signal.NotifyContext`.

See the official [`main.go`](../../cmd/flightctl-catalog-collector/main.go) for flag handling, logging, and signal handling. Custom components are compiled into the binary; YAML declarations alone do not install component code.

## Service lifecycle

Construction and execution are separate:

1. `service.New` prepares metrics and constructs every declared extension, registering each in the host.
2. It builds pipeline branches from the destination backwards through the processors, then constructs each referenced source with its downstream consumer.
3. `Service.Run` starts the metrics endpoint, starts extensions, runs destination and source preflights, and launches sources.
4. Readiness extensions are marked ready after every source goroutine enters its run wrapper, provided startup has not already been cancelled.
5. When sources finish, readiness is cleared. Successfully started extensions are shut down in reverse start order, followed by the metrics runtime.

On extension startup or preflight failure, the service cleans up the extensions that started successfully and the metrics runtime. No source runs after a failed preflight.

## Logging and metrics

Use `Settings.Logger` for component logs and `Settings.MeterProvider` for instruments. The service records source snapshot counts and pipeline synchronization metrics around your components.

Add component metrics for work the framework cannot observe, such as collection failures before a snapshot is emitted. For polling sources, `Helper.OnCollect` provides the collection result independently of downstream results.

Use stable instrument names and units, and bounded attributes such as component identifiers, resource kinds, and outcome categories. Avoid resource names, revisions, URLs, credentials, and error text as metric attributes.

For observable gauges shared across component instances, follow the shipped source's `meter.RegisterCallback` pattern. Protect values read by callbacks from concurrent updates.

See [`kubeflowmodelregistrysource/metrics.go`](../../pkg/catalogcollector/source/kubeflowmodelregistrysource/metrics.go) for an implementation and [Metrics](../user/references/catalog-collector.md#metrics) for the shipped instruments.

## Testing custom components

Cover the shared contracts and the behavior specific to your component:

| Area | What to verify |
|---|---|
| Configuration and factories | Defaults, unknown fields, validation errors, expected configuration types, and required dependencies |
| Sources | Complete snapshots, deterministic revisions, pagination failures, downstream failures, and graceful cancellation |
| Processors | Correct transformations, unchanged input including nested data, introduced collisions, and downstream error propagation |
| Destinations | Repeated snapshots, concurrent calls, pipeline isolation where resources are managed, partial failures, and target-specific deletion rules |
| Extensions | Startup and shutdown, cleanup after startup failure, capability resolution, and concurrent callers |
| Startup checks | Success, rejection, timeouts, cancellation, and failure before source execution |
| Service integration | Registration, pipeline wiring, shared instances, and isolation between branches |

Validate produced and transformed snapshots with `catalogcollector.ValidateSnapshot`. For network integrations, use controlled test servers and avoid live credentials or external service dependencies in unit tests.

Follow the repository's table-driven test conventions, with descriptive names such as "When ... it should ...". Run the relevant package tests during development and the required repository checks before committing:

```console
make unit-test
```

```console
make lint
```

Use race-enabled tests for components with concurrent calls or background work. For test tooling and other development commands, see [Building](README.md#building).

## Further reading

* [Catalog collector overview](../user/using/catalog-collector/overview.md)
* [Catalog collector configuration reference](../user/references/catalog-collector.md)
* [Installing the catalog collector](../user/installing/installing-catalog-collector.md)
