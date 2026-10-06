package catalogcollector

import "context"

// Consumer is one processing stage in a pipeline.
//
// Processors implement Consumer, transform or filter the snapshot, and forward
// it to the next Consumer supplied by their factory.
type Consumer interface {
	Consume(ctx context.Context, snapshot *CatalogSnapshot) error
}

// Source produces complete snapshots and sends them to the Consumer supplied
// by its factory.
//
// Run blocks until the context is cancelled or a fatal error occurs.
type Source interface {
	Run(ctx context.Context) error
}

// Destination is the terminal component of a pipeline.
//
// A destination may be shared by multiple pipelines. The pipelineID identifies
// whose desired state is being reconciled, allowing the destination to isolate
// ownership, drift repair, and pruning.
//
// Implementations must be safe for concurrent use.
type Destination interface {
	Reconcile(
		ctx context.Context,
		pipelineID string,
		snapshot *CatalogSnapshot,
	) error
}

// Extension provides a shared, out-of-band capability to collector
// components. Extensions are not pipeline stages and do not consume catalog
// snapshots.
//
// Examples include client authentication, shared storage, and health services.
//
// Start is called once after every configured extension has been constructed
// and before any source is started. Start performs initialization and returns;
// it must not block for the lifetime of the service.
//
// Shutdown is called once during service shutdown. Extensions are shut down in
// reverse start order.
type Extension interface {
	Start(ctx context.Context, host Host) error
	Shutdown(ctx context.Context) error
}

// Readiness is an optional capability that extensions may implement to
// participate in the service readiness lifecycle.
//
// After all extensions have started, the service discovers extensions that
// implement this interface. It calls Ready after every configured source
// goroutine has entered its run wrapper, and calls NotReady before extension
// shutdown begins.
//
// Extensions that do not implement Readiness are unaffected by the readiness
// lifecycle.
type Readiness interface {
	Ready()
	NotReady()
}

// SourcePreflight is an optional capability implemented by sources that need
// to validate external dependencies before the service becomes ready.
//
// Sources implementing this interface have their Preflight method called once
// during service startup, after extensions have started and before any source
// Run goroutine is launched.
type SourcePreflight interface {
	Preflight(ctx context.Context) error
}

// Host provides components and extensions with access to configured shared
// extensions.
//
// An extension is resolved by its complete type[/name] component ID. Callers
// must verify that the returned extension implements the capability they need.
type Host interface {
	GetExtension(id ComponentID) (Extension, error)
}
