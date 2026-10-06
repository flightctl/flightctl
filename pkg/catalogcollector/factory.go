package catalogcollector

import (
	"context"

	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/metric"
)

// ComponentType identifies the kind of component a factory creates.
// Each concrete component package defines its type as a package-level constant
// (e.g. httpsource.Type = "http"). The collector binary registers factory
// objects whose Type() methods return these values; the service matches them
// against configured component types at pipeline construction time.
type ComponentType string

// ComponentConfig is the type-erased handle for a component's typed
// configuration struct. Factories return a pointer to their concrete
// config from CreateDefaultConfig; the framework decodes raw YAML/JSON
// into it, then passes it back through the creation method. Components
// recover the concrete type with a type assertion.
type ComponentConfig = any

// Settings carries per-instance metadata that the service provides to a
// factory when constructing a component. It is separate from the
// component-specific configuration so that factories receive a stable,
// typed context without needing to parse it from the raw config.
type Settings struct {
	// ID is the parsed component identifier from the collector
	// configuration file (e.g. "http/dev-input", "flightctl/local").
	ID ComponentID

	// Host provides access to configured extensions. All factories
	// receive the same fully-populated Host instance; extensions
	// should be looked up during Start, not during construction.
	Host Host

	// Logger is a structured logger pre-scoped with component_kind and
	// component_id fields. Components should store it and use it for
	// operational log output. Never log raw configuration or credentials.
	Logger *logrus.Entry

	// MeterProvider supplies meters for recording operational metrics.
	// Components that emit metrics should create instruments from this
	// provider. The service supplies either a real provider backed by a
	// dedicated Prometheus registry or a no-op provider when metrics are
	// disabled. Components must not use the global meter provider.
	MeterProvider metric.MeterProvider
}

// Factory is the base interface shared by all component factories. Every
// factory identifies the component type it handles through its Type method
// and provides a default configuration value through CreateDefaultConfig.
type Factory interface {
	// Type returns the component type this factory creates. The returned
	// value is matched against the type portion of the component identifier
	// in the collector configuration file.
	Type() ComponentType

	// CreateDefaultConfig returns a pointer to a new instance of the
	// component's configuration struct, populated with default values.
	// The framework strict-decodes the user-supplied YAML/JSON on top of
	// this value, so any field not present in the config file retains its
	// default.
	CreateDefaultConfig() ComponentConfig
}

// SourceFactory constructs a [Source] from its parsed configuration.
//
// The service calls CreateSource after resolving the pipeline topology.
// The next consumer is the immediate downstream stage selected by the
// service — either the first processor or the terminal destination.
type SourceFactory interface {
	Factory

	// CreateSource constructs a source that delivers complete snapshots
	// through next. The source must call next.Consume for every
	// successfully produced snapshot and must handle the returned error
	// according to its protocol (e.g. returning an HTTP error to the
	// caller, retrying, or propagating the failure).
	//
	// The returned Source is not yet running; the caller starts it by
	// invoking [Source.Run].
	CreateSource(
		ctx context.Context,
		settings Settings,
		cfg ComponentConfig,
		next Consumer,
	) (Source, error)
}

// ProcessorFactory constructs a [Consumer] that transforms or filters
// snapshots before forwarding them downstream.
type ProcessorFactory interface {
	Factory

	// CreateProcessor constructs a processor that receives snapshots
	// through its Consume method, transforms or filters them, and
	// forwards the result by calling next.Consume.
	CreateProcessor(
		ctx context.Context,
		settings Settings,
		cfg ComponentConfig,
		next Consumer,
	) (Consumer, error)
}

// DestinationFactory constructs a [Destination] that reconciles catalog
// snapshots against a target system.
//
// A destination may be referenced by multiple pipelines, so the returned
// Destination must be safe for concurrent use. The service constructs it once
// per configured destination and supplies the pipeline ID on each reconciliation.
type DestinationFactory interface {
	Factory

	// CreateDestination constructs a shared terminal destination. Destinations
	// have no downstream consumer.
	CreateDestination(
		ctx context.Context,
		settings Settings,
		cfg ComponentConfig,
	) (Destination, error)
}

// ExtensionFactory constructs an [Extension] from its parsed configuration.
//
// Extensions are constructed before any pipeline component so that the
// populated [Host] can be passed to all factories. The returned extension is
// not yet started; the service calls [Extension.Start] after all extensions
// have been constructed and registered.
type ExtensionFactory interface {
	Factory

	// CreateExtension constructs an extension instance. The settings.Host
	// is fully populated with all configured extensions by the time any
	// factory is called; extension-to-extension lookups should be deferred
	// to [Extension.Start] rather than performed here.
	CreateExtension(
		ctx context.Context,
		settings Settings,
		cfg ComponentConfig,
	) (Extension, error)
}

// Factories holds the set of component factories available to a collector
// binary. The service builds private lookup maps from each factory's Type()
// method at pipeline construction time.
//
// Factories are supplied explicitly by the collector binary's main function.
// There is no global registration, init()-time side effect, or package-level
// registry. External repositories provide additional factories by compiling
// their own collector distribution that includes their factory objects
// alongside the built-in set.
type Factories struct {
	Sources      []SourceFactory
	Processors   []ProcessorFactory
	Destinations []DestinationFactory
	Extensions   []ExtensionFactory
}
