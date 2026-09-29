// Package service provides the pipeline builder and runner for the
// flightctl-catalog-collector. It wires configured pipelines from a
// parsed [config.Config] and a set of [catalogcollector.Factories], then
// supervises the running sources.
package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"

	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/errgroup"
)

// Settings carries service-level configuration for the catalog collector.
type Settings struct {
	// Logger is the root logger for the service. It must not be nil. It is
	// dedicated to the collector: service.New may change its level when
	// cfg.Service.LogLevel is explicitly set.
	// Component loggers are derived from it with component-scoped fields.
	Logger *logrus.Logger
}

type runningSource struct {
	id     catalogcollector.ComponentID
	source catalogcollector.Source
}

type runningExtension struct {
	id        catalogcollector.ComponentID
	extension catalogcollector.Extension
}

// serviceHost implements [catalogcollector.Host].
//
// Extensions are constructed and registered sequentially during New.
// Extension factories must not resolve other extensions during construction.
// When Extension.Start is called, the host contains every configured
// extension. Source, processor, and destination factories receive the
// completed host.
type serviceHost struct {
	extensions map[catalogcollector.ComponentID]catalogcollector.Extension
}

func newServiceHost() *serviceHost {
	return &serviceHost{
		extensions: make(map[catalogcollector.ComponentID]catalogcollector.Extension),
	}
}

func (h *serviceHost) register(
	id catalogcollector.ComponentID,
	extension catalogcollector.Extension,
) error {
	if extension == nil {
		return fmt.Errorf("extension %q is nil", id)
	}
	if _, exists := h.extensions[id]; exists {
		return fmt.Errorf("extension %q already registered", id)
	}
	h.extensions[id] = extension
	return nil
}

func (h *serviceHost) GetExtension(
	id catalogcollector.ComponentID,
) (catalogcollector.Extension, error) {
	extension, ok := h.extensions[id]
	if !ok {
		return nil, fmt.Errorf("no extension registered for ID %q", id)
	}
	return extension, nil
}

type pipelineBranch struct {
	pipelineID string
	consumer   catalogcollector.Consumer
}

// destinationConsumer binds one pipeline branch to a shared destination.
//
// The pipeline ID does not need to pass through every processor. It is stored
// by this terminal adapter and supplied when the snapshot reaches the shared
// destination.
type destinationConsumer struct {
	pipelineID  string
	destination catalogcollector.Destination
}

func (c *destinationConsumer) Consume(
	ctx context.Context,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	return c.destination.Reconcile(ctx, c.pipelineID, snapshot)
}

// fanoutConsumer distributes a snapshot to every pipeline branch associated
// with a configured source.
//
// Delivery is synchronous. Every branch is attempted even if another branch
// fails. Returned errors are annotated with the corresponding pipeline ID and
// joined into a single error.
//
// Snapshots are passed to every branch unchanged. Consumers must follow the
// CatalogSnapshot immutability contract. A processor that transforms a
// snapshot must copy the data it changes.
type fanoutConsumer struct {
	branches []pipelineBranch
}

func newFanoutConsumer(branches []pipelineBranch) catalogcollector.Consumer {
	return &fanoutConsumer{
		branches: slices.Clone(branches),
	}
}

func (f *fanoutConsumer) Consume(
	ctx context.Context,
	snapshot *catalogcollector.CatalogSnapshot,
) error {
	var errs []error

	for _, branch := range f.branches {
		if err := ctx.Err(); err != nil {
			return err
		}

		if err := branch.consumer.Consume(ctx, snapshot); err != nil {
			errs = append(errs, fmt.Errorf(
				"pipeline %q: %w",
				branch.pipelineID,
				err,
			))
		}
	}

	return errors.Join(errs...)
}

// Service holds the configured source and extension instances and supervises
// their execution.
type Service struct {
	sources    []runningSource
	extensions []runningExtension
	host       *serviceHost
	log        *logrus.Logger
}

type factoryMaps struct {
	sources      map[catalogcollector.ComponentType]catalogcollector.SourceFactory
	processors   map[catalogcollector.ComponentType]catalogcollector.ProcessorFactory
	destinations map[catalogcollector.ComponentType]catalogcollector.DestinationFactory
	extensions   map[catalogcollector.ComponentType]catalogcollector.ExtensionFactory
}

// New builds the pipeline graph described by cfg using the supplied factories.
//
// Factory lookup maps are built from each factory's Type method. Duplicate
// types within the same component kind, or empty types, are rejected.
//
// Only sources, processors, and destinations referenced by at least one
// pipeline are constructed. Unreferenced definitions of those component kinds
// are ignored and do not require registered factories.
//
// Every configured extension is constructed because extensions are global
// service capabilities and may be resolved by any component through Host.
//
// Component lifecycle:
//
//   - A destination is constructed once per configured destination ID and may
//     be shared by multiple pipeline branches. Each branch receives a small
//     terminal Consumer that binds its pipeline ID to the shared destination.
//   - A processor is constructed for every occurrence in a pipeline because
//     each processor instance is bound to that branch's next consumer.
//   - A source is constructed once per configured source ID.
//   - Every source receives one fan-out consumer containing all pipeline
//     branches that reference that source. A single-pipeline source still
//     receives a one-branch fan-out.
//
// Pipeline branches are built backwards: destination first, followed by
// processors in reverse order. After every branch has been built, branches
// are grouped by source and each unique source is constructed once.
//
// Errors identify pipelines and component IDs but never include raw component
// configuration, which may contain secrets.
func New(
	ctx context.Context,
	cfg *config.Config,
	factories catalogcollector.Factories,
	settings Settings,
) (*Service, error) {
	if cfg == nil {
		return nil, fmt.Errorf("collector config must not be nil")
	}
	if settings.Logger == nil {
		return nil, fmt.Errorf("service settings: Logger must not be nil")
	}

	if cfg.Service.LogLevel != nil {
		levelValue := *cfg.Service.LogLevel
		switch levelValue {
		case "debug", "info", "warn", "error":
		default:
			return nil, fmt.Errorf(
				"invalid service.logLevel %q: expected debug, info, warn, or error",
				levelValue,
			)
		}

		level, err := logrus.ParseLevel(levelValue)
		if err != nil {
			return nil, fmt.Errorf(
				"parsing service.logLevel %q: %w",
				levelValue,
				err,
			)
		}
		settings.Logger.SetLevel(level)
	}

	log := settings.Logger
	log.Info("building pipeline graph")

	fm, err := buildFactoryMaps(factories)
	if err != nil {
		return nil, err
	}

	// Phase 0: Construct all configured extensions in deterministic order and
	// register them in the host. All factories receive this host via Settings,
	// but extension-to-extension lookups should happen during Start, not here.
	host := newServiceHost()
	extensionKeys := sortedKeys(cfg.Extensions)
	extensions := make([]runningExtension, 0, len(extensionKeys))

	for _, key := range extensionKeys {
		extensionConfig := cfg.Extensions[key]
		extensionFactory, ok := fm.extensions[extensionConfig.ID.Type]
		if !ok {
			return nil, fmt.Errorf(
				"no factory registered for extension type %q (configured as %q)",
				extensionConfig.ID.Type,
				extensionConfig.ID,
			)
		}

		typedConfig, decodeErr := decodeComponent(
			extensionFactory,
			extensionConfig,
			"extensions."+key,
		)
		if decodeErr != nil {
			return nil, fmt.Errorf(
				"extension %q: %w",
				extensionConfig.ID,
				decodeErr,
			)
		}

		extension, createErr := extensionFactory.CreateExtension(
			ctx,
			newComponentSettings(
				settings.Logger,
				host,
				"extension",
				extensionConfig.ID,
			),
			typedConfig,
		)
		if createErr != nil {
			return nil, fmt.Errorf(
				"building extension %q: %w",
				extensionConfig.ID,
				createErr,
			)
		}

		if registerErr := host.register(
			extensionConfig.ID,
			extension,
		); registerErr != nil {
			return nil, fmt.Errorf(
				"registering extension %q: %w",
				extensionConfig.ID,
				registerErr,
			)
		}

		log.WithField(
			"extension_id",
			extensionConfig.ID.String(),
		).Info("extension constructed")

		extensions = append(extensions, runningExtension{
			id:        extensionConfig.ID,
			extension: extension,
		})
	}

	if len(extensions) > 0 {
		log.WithField(
			"count",
			len(extensions),
		).Info("all extensions constructed")
	}

	pipelineNames := sortedKeys(cfg.Pipelines)

	destinations := make(map[string]catalogcollector.Destination)
	branchesBySource := make(map[string][]pipelineBranch)
	processorInstanceCount := 0

	// Phase 1: Build the consumer branch for every pipeline.
	for _, pipelineID := range pipelineNames {
		pipelineConfig := cfg.Pipelines[pipelineID]

		// Validate that the pipeline's source type has a registered factory.
		// The source itself is constructed later, after all of its branches
		// have been collected.
		sourceConfig := cfg.Sources[pipelineConfig.Source]
		if _, ok := fm.sources[sourceConfig.ID.Type]; !ok {
			return nil, fmt.Errorf(
				"pipeline %q: no factory registered for source type %q (configured as %q)",
				pipelineID,
				sourceConfig.ID.Type,
				sourceConfig.ID,
			)
		}

		// Resolve or construct the shared destination.
		destination, ok := destinations[pipelineConfig.Destination]
		if !ok {
			destinationConfig := cfg.Destinations[pipelineConfig.Destination]
			destinationFactory, factoryExists := fm.destinations[destinationConfig.ID.Type]
			if !factoryExists {
				return nil, fmt.Errorf(
					"pipeline %q: no factory registered for destination type %q (configured as %q)",
					pipelineID,
					destinationConfig.ID.Type,
					destinationConfig.ID,
				)
			}

			typedConfig, decodeErr := decodeComponent(
				destinationFactory,
				destinationConfig,
				"destinations."+pipelineConfig.Destination,
			)
			if decodeErr != nil {
				return nil, fmt.Errorf(
					"pipeline %q: %w",
					pipelineID,
					decodeErr,
				)
			}

			destination, err = destinationFactory.CreateDestination(
				ctx,
				newComponentSettings(
					settings.Logger,
					host,
					"destination",
					destinationConfig.ID,
				),
				typedConfig,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"pipeline %q: building destination %q: %w",
					pipelineID,
					destinationConfig.ID,
					err,
				)
			}

			destinations[pipelineConfig.Destination] = destination
		}

		// Bind this pipeline ID to the shared destination. Processors remain
		// generic Consumers and do not need to carry routing metadata.
		next := catalogcollector.Consumer(&destinationConsumer{
			pipelineID:  pipelineID,
			destination: destination,
		})

		// Construct new processor instances for this pipeline in reverse order.
		for _, processorKey := range slices.Backward(pipelineConfig.Processors) {
			processorConfig := cfg.Processors[processorKey]
			processorFactory, factoryExists := fm.processors[processorConfig.ID.Type]
			if !factoryExists {
				return nil, fmt.Errorf(
					"pipeline %q: no factory registered for processor type %q (configured as %q)",
					pipelineID,
					processorConfig.ID.Type,
					processorConfig.ID,
				)
			}

			typedConfig, decodeErr := decodeComponent(
				processorFactory,
				processorConfig,
				"processors."+processorKey,
			)
			if decodeErr != nil {
				return nil, fmt.Errorf(
					"pipeline %q: %w",
					pipelineID,
					decodeErr,
				)
			}

			processorSettings := newComponentSettings(
				settings.Logger,
				host,
				"processor",
				processorConfig.ID,
			)
			processorSettings.Logger = processorSettings.Logger.WithField(
				"pipeline_id",
				pipelineID,
			)

			next, err = processorFactory.CreateProcessor(
				ctx,
				processorSettings,
				typedConfig,
				next,
			)
			if err != nil {
				return nil, fmt.Errorf(
					"pipeline %q: building processor %q: %w",
					pipelineID,
					processorConfig.ID,
					err,
				)
			}
		}

		// Collect processor component IDs in configured forward order.
		processorIDs := make([]string, len(pipelineConfig.Processors))
		for i, processorKey := range pipelineConfig.Processors {
			processorIDs[i] = cfg.Processors[processorKey].ID.String()
		}
		processorInstanceCount += len(pipelineConfig.Processors)

		log.WithFields(logrus.Fields{
			"pipeline_id":    pipelineID,
			"source_id":      sourceConfig.ID.String(),
			"processor_ids":  processorIDs,
			"destination_id": cfg.Destinations[pipelineConfig.Destination].ID.String(),
		}).Info("pipeline constructed")

		// next is now the head consumer for this pipeline: either its first
		// processor or its destination when no processors are configured.
		branchesBySource[pipelineConfig.Source] = append(
			branchesBySource[pipelineConfig.Source],
			pipelineBranch{
				pipelineID: pipelineID,
				consumer:   next,
			},
		)
	}

	// Phase 2: Construct every referenced source exactly once.
	//
	// Sorting provides deterministic construction and startup order.
	sourceKeys := sortedKeys(branchesBySource)
	sources := make([]runningSource, 0, len(sourceKeys))

	for _, sourceKey := range sourceKeys {
		sourceConfig := cfg.Sources[sourceKey]
		sourceFactory := fm.sources[sourceConfig.ID.Type]

		typedConfig, decodeErr := decodeComponent(
			sourceFactory,
			sourceConfig,
			"sources."+sourceKey,
		)
		if decodeErr != nil {
			return nil, fmt.Errorf(
				"source %q: %w",
				sourceConfig.ID,
				decodeErr,
			)
		}

		downstream := newFanoutConsumer(branchesBySource[sourceKey])

		source, createErr := sourceFactory.CreateSource(
			ctx,
			newComponentSettings(
				settings.Logger,
				host,
				"source",
				sourceConfig.ID,
			),
			typedConfig,
			downstream,
		)
		if createErr != nil {
			return nil, fmt.Errorf(
				"building source %q: %w",
				sourceConfig.ID,
				createErr,
			)
		}

		sources = append(sources, runningSource{
			id:     sourceConfig.ID,
			source: source,
		})
	}

	log.WithFields(logrus.Fields{
		"source_count":             len(sources),
		"pipeline_count":           len(pipelineNames),
		"processor_instance_count": processorInstanceCount,
		"extension_count":          len(extensions),
		"destination_count":        len(destinations),
	}).Info("pipeline graph built successfully")

	return &Service{
		sources:    sources,
		extensions: extensions,
		host:       host,
		log:        settings.Logger,
	}, nil
}

// Run starts configured extensions in deterministic order, then starts all
// source instances concurrently and blocks until they have stopped. Extensions
// are shut down in reverse start order after sources finish.
//
// If an extension fails to start, already-started readiness extensions are
// marked not ready, then all already-started extensions are shut down in
// reverse order. No source is started.
//
// If a source returns a non-nil error, the remaining sources are cancelled and
// Run returns an error identifying the failed source. Errors returned while a
// source delivers a snapshot through its fan-out already identify the failing
// pipeline.
//
// Caller cancellation is treated as graceful shutdown. Source implementations
// must stop and return nil when their context is cancelled.
func (s *Service) Run(ctx context.Context) error {
	// Start extensions in deterministic order.
	for index, extension := range s.extensions {
		s.log.WithField(
			"extension_id",
			extension.id.String(),
		).Info("starting extension")

		if err := extension.extension.Start(ctx, s.host); err != nil {
			startedExtensions := s.extensions[:index]

			for _, readiness := range s.discoverReadiness(startedExtensions) {
				readiness.NotReady()
			}

			shutdownErr := s.shutdownExtensions(
				ctx,
				startedExtensions,
			)
			return errors.Join(
				fmt.Errorf(
					"starting extension %q: %w",
					extension.id,
					err,
				),
				shutdownErr,
			)
		}
	}

	// Discover extensions that participate in the readiness lifecycle.
	readinessExtensions := s.discoverReadiness(s.extensions)

	group, groupContext := errgroup.WithContext(ctx)

	// Startup barrier: every source signals when it has entered its Run
	// wrapper. This proves that every source goroutine has been launched; it
	// does not claim that a source-specific external dependency is healthy.
	var startupBarrier sync.WaitGroup
	startupBarrier.Add(len(s.sources))

	for _, configuredSource := range s.sources {
		s.log.WithField(
			"source_id",
			configuredSource.id.String(),
		).Info("starting source")

		group.Go(func() error {
			startupBarrier.Done()

			if err := configuredSource.source.Run(groupContext); err != nil {
				return fmt.Errorf(
					"source %q failed: %w",
					configuredSource.id,
					err,
				)
			}

			s.log.WithField(
				"source_id",
				configuredSource.id.String(),
			).Info("source stopped")
			return nil
		})
	}

	// Signal readiness only after every source goroutine has entered its Run
	// wrapper and neither the caller nor the source group has already been
	// cancelled. A source that fails immediately cancels groupContext and must
	// not allow a transient ready state to be published.
	startupBarrier.Wait()
	if ctx.Err() == nil && groupContext.Err() == nil {
		for _, readiness := range readinessExtensions {
			readiness.Ready()
		}
	}

	sourceErr := group.Wait()

	// Signal not-ready before shutting down extensions, regardless of whether
	// sources stopped cleanly or with an error.
	for _, readiness := range readinessExtensions {
		readiness.NotReady()
	}

	if sourceErr != nil {
		s.log.WithError(sourceErr).
			Warn("sources stopped with error, initiating extension shutdown")
	} else {
		s.log.Info("all sources stopped, initiating extension shutdown")
	}

	shutdownErr := s.shutdownExtensions(ctx, s.extensions)
	if shutdownErr != nil {
		s.log.WithError(shutdownErr).
			Warn("extension shutdown completed with errors")
	} else {
		s.log.Info("graceful shutdown complete")
	}

	return errors.Join(sourceErr, shutdownErr)
}

// discoverReadiness returns the subset of extensions that implement
// [catalogcollector.Readiness]. The service does not import any concrete
// extension package; discovery is purely interface-based.
//
// Callers pass only extensions that have started successfully when handling a
// partial startup failure. This prevents lifecycle methods from being invoked
// on an extension whose Start method failed.
func (s *Service) discoverReadiness(
	extensions []runningExtension,
) []catalogcollector.Readiness {
	result := make(
		[]catalogcollector.Readiness,
		0,
		len(extensions),
	)

	for _, extension := range extensions {
		readiness, ok := extension.extension.(catalogcollector.Readiness)
		if !ok {
			continue
		}

		s.log.WithField(
			"extension_id",
			extension.id.String(),
		).Debug("extension participates in readiness lifecycle")

		result = append(result, readiness)
	}

	return result
}

// shutdownExtensions shuts down the given extensions in reverse order.
//
// context.WithoutCancel is used so that an already-cancelled Run context does
// not prevent orderly shutdown while still preserving context values such as
// request IDs and trace spans.
func (s *Service) shutdownExtensions(
	ctx context.Context,
	extensions []runningExtension,
) error {
	shutdownContext := context.WithoutCancel(ctx)
	var errs []error

	for index := len(extensions) - 1; index >= 0; index-- {
		extension := extensions[index]

		s.log.WithField(
			"extension_id",
			extension.id.String(),
		).Info("shutting down extension")

		if err := extension.extension.Shutdown(shutdownContext); err != nil {
			errs = append(
				errs,
				fmt.Errorf(
					"shutting down extension %q: %w",
					extension.id,
					err,
				),
			)
		}
	}

	return errors.Join(errs...)
}

func decodeComponent(
	factory catalogcollector.Factory,
	componentConfig config.ComponentConfig,
	path string,
) (catalogcollector.ComponentConfig, error) {
	defaults := factory.CreateDefaultConfig()
	if err := config.DecodeComponent(
		componentConfig.Config,
		defaults,
		path,
	); err != nil {
		return nil, err
	}

	return defaults, nil
}

func buildFactoryMaps(
	factories catalogcollector.Factories,
) (*factoryMaps, error) {
	factoryMap := &factoryMaps{
		sources: make(
			map[catalogcollector.ComponentType]catalogcollector.SourceFactory,
			len(factories.Sources),
		),
		processors: make(
			map[catalogcollector.ComponentType]catalogcollector.ProcessorFactory,
			len(factories.Processors),
		),
		destinations: make(
			map[catalogcollector.ComponentType]catalogcollector.DestinationFactory,
			len(factories.Destinations),
		),
		extensions: make(
			map[catalogcollector.ComponentType]catalogcollector.ExtensionFactory,
			len(factories.Extensions),
		),
	}

	for _, factory := range factories.Sources {
		componentType := factory.Type()
		if componentType == "" {
			return nil, fmt.Errorf("source factory has empty type")
		}
		if _, exists := factoryMap.sources[componentType]; exists {
			return nil, fmt.Errorf(
				"duplicate source factory type %q",
				componentType,
			)
		}
		factoryMap.sources[componentType] = factory
	}

	for _, factory := range factories.Processors {
		componentType := factory.Type()
		if componentType == "" {
			return nil, fmt.Errorf("processor factory has empty type")
		}
		if _, exists := factoryMap.processors[componentType]; exists {
			return nil, fmt.Errorf(
				"duplicate processor factory type %q",
				componentType,
			)
		}
		factoryMap.processors[componentType] = factory
	}

	for _, factory := range factories.Destinations {
		componentType := factory.Type()
		if componentType == "" {
			return nil, fmt.Errorf("destination factory has empty type")
		}
		if _, exists := factoryMap.destinations[componentType]; exists {
			return nil, fmt.Errorf(
				"duplicate destination factory type %q",
				componentType,
			)
		}
		factoryMap.destinations[componentType] = factory
	}

	for _, factory := range factories.Extensions {
		componentType := factory.Type()
		if componentType == "" {
			return nil, fmt.Errorf("extension factory has empty type")
		}
		if _, exists := factoryMap.extensions[componentType]; exists {
			return nil, fmt.Errorf(
				"duplicate extension factory type %q",
				componentType,
			)
		}
		factoryMap.extensions[componentType] = factory
	}

	return factoryMap, nil
}

// newComponentSettings returns component settings with a logger scoped by
// component_kind and component_id. Processor callers additionally attach
// pipeline_id because processor instances belong to one pipeline.
func newComponentSettings(
	logger *logrus.Logger,
	host catalogcollector.Host,
	kind string,
	id catalogcollector.ComponentID,
) catalogcollector.Settings {
	return catalogcollector.Settings{
		ID:   id,
		Host: host,
		Logger: logger.WithFields(logrus.Fields{
			"component_kind": kind,
			"component_id":   id.String(),
		}),
	}
}

func sortedKeys[M ~map[string]V, V any](values M) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
