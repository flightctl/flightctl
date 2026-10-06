package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/config"
	"github.com/flightctl/flightctl/pkg/catalogcollector/processor/catalognameprocessor"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

// --- Test helpers ---

// testSettings returns a Settings with a logger that discards all output.
func testSettings() Settings {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return Settings{Logger: log}
}

func cid(s string) catalogcollector.ComponentID {
	id, err := catalogcollector.ParseComponentID(s)
	if err != nil {
		panic(err)
	}
	return id
}

func cc(idStr string) config.ComponentConfig {
	return config.ComponentConfig{ID: cid(idStr)}
}

type recordingConsumer struct {
	mu        sync.Mutex
	snapshots []*catalogcollector.CatalogSnapshot
}

func (c *recordingConsumer) Consume(_ context.Context, s *catalogcollector.CatalogSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots = append(c.snapshots, s)
	return nil
}

func (c *recordingConsumer) Reconcile(_ context.Context, _ string, s *catalogcollector.CatalogSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshots = append(c.snapshots, s)
	return nil
}

type blockingSource struct {
	started chan struct{}
}

func (s *blockingSource) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

type failingSource struct {
	err error
}

func (s *failingSource) Run(_ context.Context) error {
	return s.err
}

// fakeSourceFactory implements catalogcollector.SourceFactory for tests.
type fakeSourceFactory struct {
	typeName   catalogcollector.ComponentType
	createFunc func(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error)
}

func (f *fakeSourceFactory) Type() catalogcollector.ComponentType { return f.typeName }

func (f *fakeSourceFactory) CreateDefaultConfig() catalogcollector.ComponentConfig { return nil }

func (f *fakeSourceFactory) CreateSource(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
	return f.createFunc(ctx, settings, cfg, next)
}

// fakeDestinationFactory implements catalogcollector.DestinationFactory for tests.
type fakeDestinationFactory struct {
	typeName   catalogcollector.ComponentType
	createFunc func(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Destination, error)
}

func (f *fakeDestinationFactory) Type() catalogcollector.ComponentType { return f.typeName }

func (f *fakeDestinationFactory) CreateDefaultConfig() catalogcollector.ComponentConfig { return nil }

func (f *fakeDestinationFactory) CreateDestination(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
	return f.createFunc(ctx, settings, cfg)
}

// fakeProcessorFactory implements catalogcollector.ProcessorFactory for tests.
type fakeProcessorFactory struct {
	typeName   catalogcollector.ComponentType
	createFunc func(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error)
}

func (f *fakeProcessorFactory) Type() catalogcollector.ComponentType { return f.typeName }

func (f *fakeProcessorFactory) CreateDefaultConfig() catalogcollector.ComponentConfig { return nil }

func (f *fakeProcessorFactory) CreateProcessor(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
	return f.createFunc(ctx, settings, cfg, next)
}

// recordingProcessor records received snapshots and their next consumer.
type recordingProcessor struct {
	name     string
	next     catalogcollector.Consumer
	received []*catalogcollector.CatalogSnapshot
	mu       sync.Mutex
}

func (p *recordingProcessor) Consume(ctx context.Context, s *catalogcollector.CatalogSnapshot) error {
	p.mu.Lock()
	p.received = append(p.received, s)
	p.mu.Unlock()
	return p.next.Consume(ctx, s)
}

// errorConsumer always returns an error.
type errorConsumer struct {
	err error
}

func (c *errorConsumer) Consume(_ context.Context, _ *catalogcollector.CatalogSnapshot) error {
	return c.err
}

func (c *errorConsumer) Reconcile(_ context.Context, _ string, _ *catalogcollector.CatalogSnapshot) error {
	return c.err
}

// fakeExtension records lifecycle calls for assertion in tests.
type fakeExtension struct {
	mu            sync.Mutex
	startCount    int
	shutdownCount int
	startErr      error
	shutdownErr   error
	onStart       func(host catalogcollector.Host)
	onShutdown    func()
}

func (e *fakeExtension) Start(_ context.Context, host catalogcollector.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startCount++
	if e.onStart != nil {
		e.onStart(host)
	}
	return e.startErr
}

func (e *fakeExtension) Shutdown(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.shutdownCount++
	if e.onShutdown != nil {
		e.onShutdown()
	}
	return e.shutdownErr
}

// fakeExtensionFactory implements catalogcollector.ExtensionFactory for tests.
type fakeExtensionFactory struct {
	typeName   catalogcollector.ComponentType
	createFunc func(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Extension, error)
}

func (f *fakeExtensionFactory) Type() catalogcollector.ComponentType                  { return f.typeName }
func (f *fakeExtensionFactory) CreateDefaultConfig() catalogcollector.ComponentConfig { return nil }
func (f *fakeExtensionFactory) CreateExtension(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
	return f.createFunc(ctx, settings, cfg)
}

// minimalPipelineCfg is a convenience for tests that need a valid one-pipeline
// config but don't care about extension or processor wiring.
func minimalPipelineCfg() *config.Config {
	return &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}
}

// minimalFactories returns a factories set containing the minimum source and
// destination needed to build minimalPipelineCfg without extensions.
func minimalFactories() catalogcollector.Factories {
	return catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}
}

// --- Factory validation tests ---

func TestNew_EmptySourceFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: ""},
		},
	}, testSettings())
	require.ErrorContains(t, err, "source factory has empty type")
}

func TestNew_EmptyProcessorFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: ""},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.ErrorContains(t, err, "processor factory has empty type")
}

func TestNew_EmptyDestinationFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: ""},
		},
	}, testSettings())
	require.ErrorContains(t, err, "destination factory has empty type")
}

func TestNew_DuplicateSourceFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "dup"},
			&fakeSourceFactory{typeName: "dup"},
		},
	}, testSettings())
	require.ErrorContains(t, err, `duplicate source factory type "dup"`)
}

func TestNew_DuplicateProcessorFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "dup"},
			&fakeProcessorFactory{typeName: "dup"},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.ErrorContains(t, err, `duplicate processor factory type "dup"`)
}

func TestNew_DuplicateDestinationFactoryType(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source"},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "dup"},
			&fakeDestinationFactory{typeName: "dup"},
		},
	}, testSettings())
	require.ErrorContains(t, err, `duplicate destination factory type "dup"`)
}

// --- Missing factory tests ---

func TestNew_MissingSourceFactory(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"nonexistent-source/my-src": cc("nonexistent-source/my-src")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "nonexistent-source/my-src", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.ErrorContains(t, err, `no factory registered for source type "nonexistent-source"`)
	require.ErrorContains(t, err, `"nonexistent-source/my-src"`)
}

func TestNew_MissingDestinationFactory(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"nonexistent-dest/my-dst": cc("nonexistent-dest/my-dst")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "nonexistent-dest/my-dst"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
	}, testSettings())
	require.ErrorContains(t, err, `no factory registered for destination type "nonexistent-dest"`)
	require.ErrorContains(t, err, `"nonexistent-dest/my-dst"`)
}

func TestNew_MissingProcessorFactory(t *testing.T) {
	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"nonexistent-proc/p1": cc("nonexistent-proc/p1")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"nonexistent-proc/p1"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.ErrorContains(t, err, `no factory registered for processor type "nonexistent-proc"`)
	require.ErrorContains(t, err, `"nonexistent-proc/p1"`)
}

// --- Pipeline construction tests ---

func TestNew_SettingsReceiveFullComponentID(t *testing.T) {
	var receivedID catalogcollector.ComponentID

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source/my-input": cc("fake-source/my-input")},
		Destinations: map[string]config.ComponentConfig{"fake-dest/my-output": cc("fake-dest/my-output")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source/my-input", Destination: "fake-dest/my-output"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				receivedID = settings.ID
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.Equal(t, catalogcollector.ComponentType("fake-source"), receivedID.Type)
	require.Equal(t, "my-input", receivedID.Name)
	require.Equal(t, "fake-source/my-input", receivedID.String())
}

func TestNew_PipelineWithoutProcessors(t *testing.T) {
	var receivedNext catalogcollector.Consumer
	dst := &recordingConsumer{}

	svc, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				receivedNext = next
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return dst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.Len(t, svc.sources, 1)

	// Source receives a fanoutConsumer; verify it forwards to the destination.
	snap := &catalogcollector.CatalogSnapshot{Revision: "test"}
	require.NoError(t, receivedNext.Consume(context.Background(), snap))
	require.Len(t, dst.snapshots, 1)
	require.Same(t, snap, dst.snapshots[0])
}

func TestNew_ProcessorsExecuteInConfiguredOrder(t *testing.T) {
	dst := &recordingConsumer{}
	var proc1, proc2 *recordingProcessor

	svc, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc/filter": cc("fake-proc/filter"), "fake-proc/normalize": cc("fake-proc/normalize")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"fake-proc/filter", "fake-proc/normalize"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				p := &recordingProcessor{name: settings.ID.Name, next: next}
				if settings.ID.Name == "filter" {
					proc1 = p
				} else {
					proc2 = p
				}
				return p, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return dst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.NotNil(t, svc)

	// Verify wiring: filter → normalize → destinationConsumer → destination
	require.NotNil(t, proc1)
	require.NotNil(t, proc2)
	require.Same(t, proc2, proc1.next, "first processor should receive second processor as next")

	// proc2.next is a destinationConsumer adapter; verify it forwards to dst.
	snap := &catalogcollector.CatalogSnapshot{Revision: "test"}
	require.NoError(t, proc2.next.Consume(context.Background(), snap))
	require.Len(t, dst.snapshots, 1)
	require.Same(t, snap, dst.snapshots[0])
}

func TestNew_SourceReceivesHeadConsumer(t *testing.T) {
	var receivedNext catalogcollector.Consumer
	var proc1 *recordingProcessor

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc/p1": cc("fake-proc/p1")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"fake-proc/p1"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				receivedNext = next
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				p := &recordingProcessor{name: "p1", next: next}
				proc1 = p
				return p, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	// Source receives a fanoutConsumer; verify it forwards through the processor.
	snap := &catalogcollector.CatalogSnapshot{Revision: "test"}
	require.NoError(t, receivedNext.Consume(context.Background(), snap))
	require.Len(t, proc1.received, 1)
	require.Same(t, snap, proc1.received[0])
}

func TestNew_DestinationErrorPropagatesThroughProcessors(t *testing.T) {
	dstErr := errors.New("destination failed")
	errDst := &errorConsumer{err: dstErr}

	var proc *recordingProcessor

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc/p1": cc("fake-proc/p1")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"fake-proc/p1"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				proc = &recordingProcessor{name: "p1", next: next}
				return proc, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return errDst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	err = proc.Consume(context.Background(), &catalogcollector.CatalogSnapshot{Revision: "r1"})
	require.ErrorIs(t, err, dstErr)
}

func TestNew_SharedDestinationConstructedOnce(t *testing.T) {
	var constructionCount atomic.Int32

	var receivedDownstreams []catalogcollector.Consumer
	var mu sync.Mutex
	theConsumer := &recordingConsumer{}

	svc, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a": cc("fake-source/a"),
			"fake-source/b": cc("fake-source/b"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest/shared": cc("fake-dest/shared"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a", Destination: "fake-dest/shared"},
			"pipeline-b": {Source: "fake-source/b", Destination: "fake-dest/shared"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, downstream catalogcollector.Consumer) (catalogcollector.Source, error) {
				mu.Lock()
				receivedDownstreams = append(receivedDownstreams, downstream)
				mu.Unlock()
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				constructionCount.Add(1)
				return theConsumer, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.Len(t, svc.sources, 2)
	require.Equal(t, int32(1), constructionCount.Load(), "shared destination must be constructed exactly once")

	// Each source gets its own fanoutConsumer, but both fan-outs forward
	// to the same shared destination instance.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, receivedDownstreams, 2)

	snap := &catalogcollector.CatalogSnapshot{Revision: "test"}
	require.NoError(t, receivedDownstreams[0].Consume(context.Background(), snap))
	require.NoError(t, receivedDownstreams[1].Consume(context.Background(), snap))
	require.Len(t, theConsumer.snapshots, 2, "both fan-outs must forward to the shared destination")
}

func TestNew_ProcessorInstancesPerPipeline(t *testing.T) {
	var procInstances []catalogcollector.Consumer
	var mu sync.Mutex

	_, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a": cc("fake-source/a"),
			"fake-source/b": cc("fake-source/b"),
		},
		Processors: map[string]config.ComponentConfig{
			"fake-proc/shared": cc("fake-proc/shared"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": cc("fake-dest"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a", Processors: []string{"fake-proc/shared"}, Destination: "fake-dest"},
			"pipeline-b": {Source: "fake-source/b", Processors: []string{"fake-proc/shared"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				p := &recordingProcessor{next: next}
				mu.Lock()
				procInstances = append(procInstances, p)
				mu.Unlock()
				return p, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, procInstances, 2, "processor must be constructed per pipeline")
	require.NotSame(t, procInstances[0], procInstances[1], "each pipeline must get its own processor instance")
}

func TestNew_UnusedComponentsNotConstructed(t *testing.T) {
	var srcConstructions, dstConstructions atomic.Int32

	_, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/used":   cc("fake-source/used"),
			"unused-type/unused": cc("unused-type/unused"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest/used":     cc("fake-dest/used"),
			"unused-type/unused": cc("unused-type/unused"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"only-pipeline": {Source: "fake-source/used", Destination: "fake-dest/used"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcConstructions.Add(1)
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				dstConstructions.Add(1)
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.Equal(t, int32(1), srcConstructions.Load())
	require.Equal(t, int32(1), dstConstructions.Load())
}

// --- Runtime tests ---

func TestRun_SourcesRunConcurrently(t *testing.T) {
	startedA := make(chan struct{})
	startedB := make(chan struct{})

	srcA := &blockingSource{started: startedA}
	srcB := &blockingSource{started: startedB}

	sourceIndex := 0
	sources := []catalogcollector.Source{srcA, srcB}
	var mu sync.Mutex

	svc, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a": cc("fake-source/a"),
			"fake-source/b": cc("fake-source/b"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": cc("fake-dest"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a", Destination: "fake-dest"},
			"pipeline-b": {Source: "fake-source/b", Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				mu.Lock()
				src := sources[sourceIndex]
				sourceIndex++
				mu.Unlock()
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-startedA:
	case <-time.After(2 * time.Second):
		t.Fatal("source A did not start")
	}
	select {
	case <-startedB:
	case <-time.After(2 * time.Second):
		t.Fatal("source B did not start")
	}

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestRun_FatalSourceCancelsOthers(t *testing.T) {
	fatalErr := errors.New("connection refused")

	svc, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"blocking/good": cc("blocking/good"),
			"failing/bad":   cc("failing/bad"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": cc("fake-dest"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-good": {Source: "blocking/good", Destination: "fake-dest"},
			"pipeline-bad":  {Source: "failing/bad", Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "blocking", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
			&fakeSourceFactory{typeName: "failing", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &failingSource{err: fatalErr}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- svc.Run(context.Background()) }()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorContains(t, err, "source")
		require.ErrorContains(t, err, "failed")
		require.ErrorIs(t, err, fatalErr)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after source failure")
	}
}

// TestNew_CatalognameProcessorWiring verifies that the real catalognameprocessor
// factory decodes its JSON config, renames catalogs and items, and forwards
// the transformed snapshot to the destination in order.
func TestNew_CatalognameProcessorWiring(t *testing.T) {
	mappingsJSON, err := json.Marshal(map[string]any{
		"mappings": map[string]string{"upstream-models": "stage-models"},
	})
	require.NoError(t, err)

	dst := &recordingConsumer{}

	// Capture the processor-chain head passed to the source so we can drive it directly.
	var head catalogcollector.Consumer
	svc, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source": {ID: cid("fake-source")},
		},
		Processors: map[string]config.ComponentConfig{
			"catalogname/rename": {ID: cid("catalogname/rename"), Config: json.RawMessage(mappingsJSON)},
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": {ID: cid("fake-dest")},
		},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"catalogname/rename"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				head = next
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			catalognameprocessor.NewFactory(),
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return dst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.NotNil(t, head)

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "r1",
		Catalogs: []apiv1alpha1.Catalog{
			{Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("upstream-models")}},
		},
		CatalogItems: []apiv1alpha1.CatalogItem{
			{
				Metadata: apiv1alpha1.CatalogItemMeta{Catalog: "upstream-models", Name: lo.ToPtr("item-a")},
				Spec: apiv1alpha1.CatalogItemSpec{
					Type: apiv1alpha1.CatalogItemTypeData,
					Artifacts: []apiv1alpha1.CatalogItemArtifact{
						{Type: apiv1alpha1.CatalogItemArtifactTypeContainer, Uri: "example.com/repo"},
					},
					Versions: []apiv1alpha1.CatalogItemVersion{
						{
							Version:  "1.0.0",
							Channels: []string{"stable"},
							References: map[apiv1alpha1.CatalogItemArtifactType]string{
								apiv1alpha1.CatalogItemArtifactTypeContainer: "sha256:abc123",
							},
						},
					},
				},
			},
		},
	}
	require.NoError(t, head.Consume(context.Background(), snap))

	require.Len(t, dst.snapshots, 1)
	out := dst.snapshots[0]
	require.Len(t, out.Catalogs, 1)
	assert.Equal(t, "stage-models", *out.Catalogs[0].Metadata.Name, "catalog must be renamed")
	require.Len(t, out.CatalogItems, 1)
	assert.Equal(t, "stage-models", out.CatalogItems[0].Metadata.Catalog, "catalog item catalog field must be renamed")

	// Original snapshot must not be mutated.
	assert.Equal(t, "upstream-models", *snap.Catalogs[0].Metadata.Name)
	assert.Equal(t, "upstream-models", snap.CatalogItems[0].Metadata.Catalog)
}

// =============================================================================
// Extension factory validation tests
// =============================================================================

func TestNew_EmptyExtensionFactoryType(t *testing.T) {
	cfg := minimalPipelineCfg()
	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: ""},
	}
	_, err := New(context.Background(), cfg, factories, testSettings())
	require.ErrorContains(t, err, "extension factory has empty type")
}

func TestNew_DuplicateExtensionFactoryType(t *testing.T) {
	cfg := minimalPipelineCfg()
	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "dup"},
		&fakeExtensionFactory{typeName: "dup"},
	}
	_, err := New(context.Background(), cfg, factories, testSettings())
	require.ErrorContains(t, err, `duplicate extension factory type "dup"`)
}

func TestNew_MissingExtensionFactory(t *testing.T) {
	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"nonexistent-ext/my-ext": cc("nonexistent-ext/my-ext"),
	}
	factories := minimalFactories()
	_, err := New(context.Background(), cfg, factories, testSettings())
	require.ErrorContains(t, err, `no factory registered for extension type "nonexistent-ext"`)
	require.ErrorContains(t, err, `"nonexistent-ext/my-ext"`)
}

// =============================================================================
// Host lookup tests
// =============================================================================

func TestServiceHost_GetExtension_Found(t *testing.T) {
	ext := &fakeExtension{}
	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"auth/keycloak": cc("auth/keycloak")}

	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return ext, nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	got, err := svc.host.GetExtension(cid("auth/keycloak"))
	require.NoError(t, err)
	require.Same(t, ext, got)
}

func TestServiceHost_GetExtension_Missing(t *testing.T) {
	h := newServiceHost()
	_, err := h.GetExtension(cid("auth/missing"))
	require.Error(t, err)
	require.ErrorContains(t, err, "auth/missing")
}

func TestServiceHost_Register_NilExtension(t *testing.T) {
	h := newServiceHost()
	err := h.register(cid("auth/x"), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "nil")
}

func TestServiceHost_Register_Duplicate(t *testing.T) {
	h := newServiceHost()
	ext := &fakeExtension{}
	require.NoError(t, h.register(cid("auth/x"), ext))
	err := h.register(cid("auth/x"), ext)
	require.Error(t, err)
	require.ErrorContains(t, err, "already registered")
}

// =============================================================================
// Host availability during pipeline construction
// =============================================================================

func TestNew_HostAvailableDuringPipelineConstruction(t *testing.T) {
	ext := &fakeExtension{}
	extID := cid("auth/keycloak")

	var hostSeenBySource, hostSeenByDest, hostSeenByProc catalogcollector.Host

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc": cc("fake-proc")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"auth/keycloak": cc("auth/keycloak")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Processors: []string{"fake-proc"}, Destination: "fake-dest"}},
	}

	_, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				hostSeenBySource = settings.Host
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				hostSeenByProc = settings.Host
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				hostSeenByDest = settings.Host
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return ext, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	for _, host := range []catalogcollector.Host{hostSeenBySource, hostSeenByDest, hostSeenByProc} {
		require.NotNil(t, host, "all factories must receive a non-nil Host")
		got, lookupErr := host.GetExtension(extID)
		require.NoError(t, lookupErr)
		require.Same(t, ext, got, "all factories receive the same populated Host")
	}
}

// =============================================================================
// Extension lifecycle: start order, shutdown order, failure handling
// =============================================================================

func TestRun_ExtensionsStartInDeterministicOrder(t *testing.T) {
	var mu sync.Mutex
	var startOrder []string

	makeExt := func(name string) *fakeExtension {
		e := &fakeExtension{}
		e.onStart = func(_ catalogcollector.Host) {
			mu.Lock()
			startOrder = append(startOrder, name)
			mu.Unlock()
		}
		return e
	}

	extB := makeExt("b-ext")
	extA := makeExt("a-ext")

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		// Intentionally add 'b' before 'a' to verify sorted ordering.
		"myext/b-ext": cc("myext/b-ext"),
		"myext/a-ext": cc("myext/a-ext"),
	}

	extMap := map[string]*fakeExtension{
		"a-ext": extA,
		"b-ext": extB,
	}

	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "myext", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return extMap[settings.ID.Name], nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for both extensions to start.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(startOrder) == 2
	}, 2*time.Second, 5*time.Millisecond, "both extensions must start")
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// Keys "myext/a-ext" < "myext/b-ext" lexicographically.
	require.Equal(t, []string{"a-ext", "b-ext"}, startOrder)
}

func TestRun_ExtensionsShutdownInReverseOrder(t *testing.T) {
	var mu sync.Mutex
	var shutdownOrder []string

	makeExt := func(name string) *fakeExtension {
		e := &fakeExtension{}
		e.onShutdown = func() {
			mu.Lock()
			shutdownOrder = append(shutdownOrder, name)
			mu.Unlock()
		}
		return e
	}

	extA := makeExt("a-ext")
	extB := makeExt("b-ext")

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"myext/a-ext": cc("myext/a-ext"),
		"myext/b-ext": cc("myext/b-ext"),
	}

	extMap := map[string]*fakeExtension{"a-ext": extA, "b-ext": extB}

	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "myext", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return extMap[settings.ID.Name], nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for both extensions to have started before cancelling.
	require.Eventually(t, func() bool {
		extA.mu.Lock()
		aStarted := extA.startCount > 0
		extA.mu.Unlock()
		extB.mu.Lock()
		bStarted := extB.startCount > 0
		extB.mu.Unlock()
		return aStarted && bStarted
	}, 2*time.Second, 5*time.Millisecond, "both extensions must start")
	cancel()
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()
	// Start order: a-ext, b-ext. Shutdown must be reverse: b-ext, a-ext.
	require.Equal(t, []string{"b-ext", "a-ext"}, shutdownOrder)
}

func TestRun_StartFailureShutdownsPreviousExtensions(t *testing.T) {
	startErr := errors.New("auth init failed")
	var mu sync.Mutex
	var shutdownOrder []string

	makeExt := func(name string, failStart bool) *fakeExtension {
		e := &fakeExtension{}
		if failStart {
			e.startErr = startErr
		}
		e.onShutdown = func() {
			mu.Lock()
			shutdownOrder = append(shutdownOrder, name)
			mu.Unlock()
		}
		return e
	}

	extA := makeExt("a-ext", false) // starts successfully
	extB := makeExt("b-ext", true)  // fails to start
	extC := makeExt("c-ext", false) // must never be started or shut down

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"myext/a-ext": cc("myext/a-ext"),
		"myext/b-ext": cc("myext/b-ext"),
		"myext/c-ext": cc("myext/c-ext"),
	}

	extMap := map[string]*fakeExtension{"a-ext": extA, "b-ext": extB, "c-ext": extC}

	factories := minimalFactories()
	// Replace the blocking source with one that confirms no sources are started
	// when extension startup fails.
	factories.Sources = []catalogcollector.SourceFactory{
		&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
			return &blockingSource{started: make(chan struct{})}, nil
		}},
	}
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "myext", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return extMap[settings.ID.Name], nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.Error(t, runErr)
	require.ErrorIs(t, runErr, startErr)

	extA.mu.Lock()
	extB.mu.Lock()
	extC.mu.Lock()
	aStart := extA.startCount
	bStart := extB.startCount
	cStart := extC.startCount
	aShutdown := extA.shutdownCount
	bShutdown := extB.shutdownCount
	cShutdown := extC.shutdownCount
	extA.mu.Unlock()
	extB.mu.Unlock()
	extC.mu.Unlock()

	require.Equal(t, 1, aStart, "a-ext must be started once")
	require.Equal(t, 1, bStart, "b-ext must be started (and fail)")
	require.Equal(t, 0, cStart, "c-ext must never be started")
	require.Equal(t, 1, aShutdown, "a-ext must be shut down after b-ext fails")
	require.Equal(t, 0, bShutdown, "b-ext must not be shut down (it never completed Start)")
	require.Equal(t, 0, cShutdown, "c-ext must never be shut down")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"a-ext"}, shutdownOrder)
}

func TestRun_ExtensionsShutdownAfterSourceError(t *testing.T) {
	fatalErr := errors.New("connection refused")
	ext := &fakeExtension{}

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"failing-source": cc("failing-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"auth": cc("auth")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "failing-source", Destination: "fake-dest"}},
	}

	svc, buildErr := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "failing-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &failingSource{err: fatalErr}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return ext, nil
			}},
		},
	}, testSettings())
	require.NoError(t, buildErr)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, fatalErr)

	ext.mu.Lock()
	shutdownCount := ext.shutdownCount
	ext.mu.Unlock()
	require.Equal(t, 1, shutdownCount, "extension must be shut down exactly once after source error")
}

func TestRun_ExtensionsShutdownAfterCancellation(t *testing.T) {
	ext := &fakeExtension{}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"auth": cc("auth")}

	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return ext, nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the extension to have started before cancelling.
	require.Eventually(t, func() bool {
		ext.mu.Lock()
		defer ext.mu.Unlock()
		return ext.startCount > 0
	}, 2*time.Second, 5*time.Millisecond, "extension must start")
	cancel()
	require.NoError(t, <-done)

	ext.mu.Lock()
	shutdownCount := ext.shutdownCount
	ext.mu.Unlock()
	require.Equal(t, 1, shutdownCount, "extension must be shut down once after cancellation")
}

func TestRun_ShutdownErrorJoinedWithSourceError(t *testing.T) {
	fatalErr := errors.New("source fatal")
	shutErr := errors.New("shutdown failed")
	ext := &fakeExtension{shutdownErr: shutErr}

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"failing-source": cc("failing-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"auth": cc("auth")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "failing-source", Destination: "fake-dest"}},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "failing-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &failingSource{err: fatalErr}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return ext, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, fatalErr)
	require.ErrorIs(t, runErr, shutErr)
}

func TestRun_ExtensionShutdownCalledExactlyOnce(t *testing.T) {
	ext := &fakeExtension{}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"auth": cc("auth")}

	factories := minimalFactories()
	factories.Extensions = []catalogcollector.ExtensionFactory{
		&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
			return ext, nil
		}},
	}

	svc, err := New(context.Background(), cfg, factories, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the extension to have started before cancelling.
	require.Eventually(t, func() bool {
		ext.mu.Lock()
		defer ext.mu.Unlock()
		return ext.startCount > 0
	}, 2*time.Second, 5*time.Millisecond, "extension must start")
	cancel()
	require.NoError(t, <-done)

	ext.mu.Lock()
	shutdownCount := ext.shutdownCount
	ext.mu.Unlock()
	require.Equal(t, 1, shutdownCount, "Shutdown must be called exactly once")
}

// =============================================================================
// Unchanged existing pipeline behavior with no extensions
// =============================================================================

func TestRun_NoExtensions_ExistingBehaviorPreserved(t *testing.T) {
	started := make(chan struct{})
	svc, err := New(context.Background(), minimalPipelineCfg(), catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: started}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// =============================================================================
// Logging infrastructure tests
// =============================================================================

func TestNew_NilLoggerRejected(t *testing.T) {
	_, err := New(context.Background(), minimalPipelineCfg(), minimalFactories(), Settings{Logger: nil})
	require.Error(t, err)
	require.ErrorContains(t, err, "Logger must not be nil")
}

func TestNew_FactoryReceivesNonNilLogger(t *testing.T) {
	var srcLogger, dstLogger *logrus.Entry

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcLogger = settings.Logger
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				dstLogger = settings.Logger
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.NotNil(t, srcLogger, "source must receive a non-nil logger")
	require.NotNil(t, dstLogger, "destination must receive a non-nil logger")
}

func TestNew_LoggerHasCorrectComponentFields(t *testing.T) {
	var srcLogger *logrus.Entry

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source/my-input": cc("fake-source/my-input")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source/my-input", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcLogger = settings.Logger
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	data := srcLogger.Data
	require.Equal(t, "source", data["component_kind"], "component_kind must be 'source'")
	require.Equal(t, "fake-source/my-input", data["component_id"], "component_id must be the full component ID")
}

func TestNew_ProcessorLoggerIncludesPipelineID(t *testing.T) {
	var procLogger *logrus.Entry

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc/p1": cc("fake-proc/p1")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"my-pipeline": {Source: "fake-source", Processors: []string{"fake-proc/p1"}, Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				procLogger = settings.Logger
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	data := procLogger.Data
	require.Equal(t, "processor", data["component_kind"])
	require.Equal(t, "fake-proc/p1", data["component_id"])
	require.Equal(t, "my-pipeline", data["pipeline_id"], "processor logger must include pipeline_id")
}

func TestNew_SharedSourceReceivesSameLoggerBase(t *testing.T) {
	var loggers []*logrus.Entry
	var mu sync.Mutex

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source/shared": cc("fake-source/shared")},
		Destinations: map[string]config.ComponentConfig{"fake-dest/a": cc("fake-dest/a"), "fake-dest/b": cc("fake-dest/b")},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/shared", Destination: "fake-dest/a"},
			"pipeline-b": {Source: "fake-source/shared", Destination: "fake-dest/b"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				mu.Lock()
				loggers = append(loggers, settings.Logger)
				mu.Unlock()
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	// The shared source is constructed exactly once, so exactly one logger is captured.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, loggers, 1, "shared source must be constructed exactly once")
	require.NotNil(t, loggers[0])
}

// =============================================================================
// Log-level override tests
// =============================================================================

func ptrStr(s string) *string { return &s }

// levelSettings returns a Settings whose logger is set to the given initial level.
func levelSettings(initial logrus.Level) Settings {
	log := logrus.New()
	log.SetOutput(io.Discard)
	log.SetLevel(initial)
	return Settings{Logger: log}
}

func TestNew_LogLevelAbsentPreservesExistingLevel(t *testing.T) {
	cfg := minimalPipelineCfg()
	// cfg.Service.LogLevel is nil by default
	settings := levelSettings(logrus.WarnLevel)
	_, err := New(context.Background(), cfg, minimalFactories(), settings)
	require.NoError(t, err)
	require.Equal(t, logrus.WarnLevel, settings.Logger.GetLevel(),
		"absent logLevel must not change the logger level")
}

func TestNew_EmptyServiceBlockPreservesLevel(t *testing.T) {
	cfg := minimalPipelineCfg()
	cfg.Service = config.ServiceConfig{} // empty, LogLevel is nil
	settings := levelSettings(logrus.ErrorLevel)
	_, err := New(context.Background(), cfg, minimalFactories(), settings)
	require.NoError(t, err)
	require.Equal(t, logrus.ErrorLevel, settings.Logger.GetLevel())
}

func TestNew_LogLevelOverride(t *testing.T) {
	cases := []struct {
		level    string
		expected logrus.Level
	}{
		{"debug", logrus.DebugLevel},
		{"info", logrus.InfoLevel},
		{"warn", logrus.WarnLevel},
		{"error", logrus.ErrorLevel},
	}

	for _, tc := range cases {
		t.Run("When logLevel is "+tc.level+" it should be applied", func(t *testing.T) {
			cfg := minimalPipelineCfg()
			cfg.Service = config.ServiceConfig{LogLevel: ptrStr(tc.level)}
			settings := levelSettings(logrus.PanicLevel) // start far from the target
			_, err := New(context.Background(), cfg, minimalFactories(), settings)
			require.NoError(t, err)
			require.Equal(t, tc.expected, settings.Logger.GetLevel())
		})
	}
}

func TestNew_LogLevelUnsupported(t *testing.T) {
	for _, bad := range []string{"", "trace", "fatal", "verbose", "WARN"} {
		t.Run("When logLevel is "+bad+" it should be rejected", func(t *testing.T) {
			cfg := minimalPipelineCfg()
			cfg.Service = config.ServiceConfig{LogLevel: ptrStr(bad)}
			_, err := New(context.Background(), cfg, minimalFactories(), testSettings())
			require.Error(t, err)
			require.ErrorContains(t, err, "service.logLevel")
		})
	}
}

func TestNew_ComponentLoggersInheritConfiguredLevel(t *testing.T) {
	var srcLogger *logrus.Entry

	cfg := minimalPipelineCfg()
	cfg.Service = config.ServiceConfig{LogLevel: ptrStr("debug")}

	settings := levelSettings(logrus.ErrorLevel) // starts at error

	_, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcLogger = settings.Logger
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)
	require.NotNil(t, srcLogger)
	require.Equal(t, logrus.DebugLevel, srcLogger.Logger.GetLevel(),
		"component logger must reflect the overridden base level")
}

// =============================================================================
// Pipeline construction and summary log tests
// =============================================================================

// logHook captures log entries for test assertions.
type logHook struct {
	mu      sync.Mutex
	entries []logrus.Entry
}

func (h *logHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *logHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, *entry)
	return nil
}

func (h *logHook) findEntries(msg string) []logrus.Entry {
	h.mu.Lock()
	defer h.mu.Unlock()
	var found []logrus.Entry
	for _, e := range h.entries {
		if e.Message == msg {
			found = append(found, e)
		}
	}
	return found
}

// testSettingsWithHook returns Settings with a log hook for inspecting entries.
func testSettingsWithHook() (Settings, *logHook) {
	log := logrus.New()
	log.SetOutput(io.Discard)
	log.SetLevel(logrus.DebugLevel)
	hook := &logHook{}
	log.AddHook(hook)
	return Settings{Logger: log}, hook
}

func TestNew_PipelineConstructedLogContainsAllFields(t *testing.T) {
	settings, hook := testSettingsWithHook()

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"http/dev-input": cc("http/dev-input")},
		Processors:   map[string]config.ComponentConfig{"catalogname/dev": cc("catalogname/dev")},
		Destinations: map[string]config.ComponentConfig{"debug/local": cc("debug/local")},
		Pipelines: map[string]config.PipelineConfig{
			"debug": {Source: "http/dev-input", Processors: []string{"catalogname/dev"}, Destination: "debug/local"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "http", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "catalogname", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "debug", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)

	entries := hook.findEntries("pipeline constructed")
	require.Len(t, entries, 1)
	entry := entries[0]
	assert.Equal(t, logrus.InfoLevel, entry.Level)
	assert.Equal(t, "debug", entry.Data["pipeline_id"])
	assert.Equal(t, "http/dev-input", entry.Data["source_id"])
	assert.Equal(t, []string{"catalogname/dev"}, entry.Data["processor_ids"])
	assert.Equal(t, "debug/local", entry.Data["destination_id"])
}

func TestNew_PipelineConstructedLogProcessorIDsInForwardOrder(t *testing.T) {
	settings, hook := testSettingsWithHook()

	_, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors: map[string]config.ComponentConfig{
			"fake-proc/first":  cc("fake-proc/first"),
			"fake-proc/second": cc("fake-proc/second"),
			"fake-proc/third":  cc("fake-proc/third"),
		},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {
				Source:      "fake-source",
				Processors:  []string{"fake-proc/first", "fake-proc/second", "fake-proc/third"},
				Destination: "fake-dest",
			},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)

	entries := hook.findEntries("pipeline constructed")
	require.Len(t, entries, 1)
	// Processors are constructed in reverse order (third, second, first) but
	// the log must list them in the configured forward order.
	assert.Equal(t,
		[]string{"fake-proc/first", "fake-proc/second", "fake-proc/third"},
		entries[0].Data["processor_ids"],
	)
}

func TestNew_PipelineConstructedLogEmptyProcessorList(t *testing.T) {
	settings, hook := testSettingsWithHook()

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)

	entries := hook.findEntries("pipeline constructed")
	require.Len(t, entries, 1)
	assert.Equal(t, []string{}, entries[0].Data["processor_ids"],
		"pipeline without processors must log an empty processor_ids list")
}

func TestNew_ProcessorInstanceCountInSummaryLog(t *testing.T) {
	settings, hook := testSettingsWithHook()

	// Two pipelines each reference the same processor configuration key.
	// Processor instances are created per-pipeline, so the instance count
	// must be 2, not 1 (the number of unique processor config keys).
	_, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a": cc("fake-source/a"),
			"fake-source/b": cc("fake-source/b"),
		},
		Processors: map[string]config.ComponentConfig{
			"fake-proc/shared": cc("fake-proc/shared"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": cc("fake-dest"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a", Processors: []string{"fake-proc/shared"}, Destination: "fake-dest"},
			"pipeline-b": {Source: "fake-source/b", Processors: []string{"fake-proc/shared"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)

	entries := hook.findEntries("pipeline graph built successfully")
	require.Len(t, entries, 1)
	data := entries[0].Data
	// Two pipeline occurrences of the same processor config = 2 instances.
	assert.Equal(t, 2, data["processor_instance_count"],
		"processor_instance_count must count per-pipeline instances, not unique config keys")
	assert.Equal(t, 2, data["source_count"])
	assert.Equal(t, 2, data["pipeline_count"])
	assert.Equal(t, 1, data["destination_count"])
	assert.Equal(t, 0, data["extension_count"])
}

func TestNew_SummaryLogDestinationCount(t *testing.T) {
	settings, hook := testSettingsWithHook()

	_, err := New(context.Background(), &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a": cc("fake-source/a"),
			"fake-source/b": cc("fake-source/b"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest/x": cc("fake-dest/x"),
			"fake-dest/y": cc("fake-dest/y"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a", Destination: "fake-dest/x"},
			"pipeline-b": {Source: "fake-source/b", Destination: "fake-dest/y"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, settings)
	require.NoError(t, err)

	entries := hook.findEntries("pipeline graph built successfully")
	require.Len(t, entries, 1)
	assert.Equal(t, 2, entries[0].Data["destination_count"],
		"destination_count must reflect len(destinations)")
}

// =============================================================================
// Readiness lifecycle tests
//
// These tests verify the readiness signaling integration without importing any
// concrete extension package. A generic fakeReadinessExtension implements both
// catalogcollector.Extension and catalogcollector.Readiness.
// =============================================================================

// fakeReadinessExtension implements Extension and Readiness for tests.
type fakeReadinessExtension struct {
	mu          sync.Mutex
	startCount  int
	shutdownSeq *[]string
	readyCalls  []string // "ready" or "not_ready" appended in order
	startErr    error
	shutdownErr error
	onStart     func(host catalogcollector.Host)
}

func (e *fakeReadinessExtension) Start(_ context.Context, host catalogcollector.Host) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.startCount++
	if e.onStart != nil {
		e.onStart(host)
	}
	return e.startErr
}

func (e *fakeReadinessExtension) Shutdown(_ context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.shutdownSeq != nil {
		*e.shutdownSeq = append(*e.shutdownSeq, "shutdown")
	}
	return e.shutdownErr
}

func (e *fakeReadinessExtension) Ready() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.readyCalls = append(e.readyCalls, "ready")
}

func (e *fakeReadinessExtension) NotReady() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.readyCalls = append(e.readyCalls, "not_ready")
}

func (e *fakeReadinessExtension) getReadyCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]string, len(e.readyCalls))
	copy(result, e.readyCalls)
	return result
}

// readinessExtensionFactory wraps fakeReadinessExtension in a factory.
type readinessExtensionFactory struct {
	typeName   catalogcollector.ComponentType
	createFunc func(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Extension, error)
}

func (f *readinessExtensionFactory) Type() catalogcollector.ComponentType { return f.typeName }
func (f *readinessExtensionFactory) CreateDefaultConfig() catalogcollector.ComponentConfig {
	return nil
}
func (f *readinessExtensionFactory) CreateExtension(ctx context.Context, settings catalogcollector.Settings, cfg catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
	return f.createFunc(ctx, settings, cfg)
}

func TestRun_ReadinessMarkedAfterSourceRunLoopsLaunched(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	sourceStarted := make(chan struct{})

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"healthext": cc("healthext")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the source to start its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// After the source has entered its run loop, the service should
	// have signaled Ready. Allow a brief moment for the barrier to
	// complete and Ready to be called.
	require.Eventually(t, func() bool {
		calls := readinessExt.getReadyCalls()
		for _, c := range calls {
			if c == "ready" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Ready must be called after source enters run loop")

	cancel()
	require.NoError(t, <-done)
}

func TestRun_NotReadyBeforeExtensionShutdown(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	var shutdownSeq []string
	readinessExt.shutdownSeq = &shutdownSeq
	sourceStarted := make(chan struct{})

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"healthext": cc("healthext")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the source to enter its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// Wait for Ready to be called after the startup barrier completes.
	require.Eventually(t, func() bool {
		for _, c := range readinessExt.getReadyCalls() {
			if c == "ready" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Ready must be called after source enters run loop")

	cancel()
	require.NoError(t, <-done)

	calls := readinessExt.getReadyCalls()
	// The sequence must be: ..., "ready", ..., "not_ready"
	// Verify that not_ready appears after ready and before or at shutdown.
	readyIdx := -1
	notReadyIdx := -1
	for i, c := range calls {
		if c == "ready" && readyIdx == -1 {
			readyIdx = i
		}
		if c == "not_ready" {
			notReadyIdx = i
		}
	}
	require.NotEqual(t, -1, readyIdx, "Ready must have been called")
	require.NotEqual(t, -1, notReadyIdx, "NotReady must have been called")
	assert.Greater(t, notReadyIdx, readyIdx, "NotReady must be called after Ready")
}

func TestRun_FatalSourceErrorTransitionsToNotReady(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	fatalErr := errors.New("source crashed")

	cfg := &config.Config{
		Sources: map[string]config.ComponentConfig{
			"blocking/good": cc("blocking/good"),
			"failing/bad":   cc("failing/bad"),
		},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"healthext": cc("healthext")},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-good": {Source: "blocking/good", Destination: "fake-dest"},
			"pipeline-bad":  {Source: "failing/bad", Destination: "fake-dest"},
		},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "blocking", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
			&fakeSourceFactory{typeName: "failing", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &failingSource{err: fatalErr}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, fatalErr)

	calls := readinessExt.getReadyCalls()
	// The last call must be not_ready.
	require.NotEmpty(t, calls, "readiness calls must not be empty")
	assert.Equal(t, "not_ready", calls[len(calls)-1], "last readiness call must be not_ready after source error")
}

func TestRun_CallerCancellationTransitionsToNotReady(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	sourceStarted := make(chan struct{})

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"healthext": cc("healthext")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the source to enter its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// Wait for Ready to be called after the startup barrier completes.
	require.Eventually(t, func() bool {
		for _, c := range readinessExt.getReadyCalls() {
			if c == "ready" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Ready must be called after source enters run loop")

	cancel()
	require.NoError(t, <-done)

	calls := readinessExt.getReadyCalls()
	require.NotEmpty(t, calls, "readiness calls must not be empty")
	assert.Equal(t, "not_ready", calls[len(calls)-1], "last readiness call must be not_ready after cancellation")
}

func TestRun_ExtensionsNotImplementingReadinessUnaffected(t *testing.T) {
	// Use a plain fakeExtension (no Readiness interface) alongside a Readiness
	// extension. The service must not panic or skip the plain extension.
	plainExt := &fakeExtension{}
	readinessExt := &fakeReadinessExtension{}
	sourceStarted := make(chan struct{})

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"plain/a":  cc("plain/a"),
		"health/b": cc("health/b"),
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "plain", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return plainExt, nil
			}},
			&readinessExtensionFactory{typeName: "health", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the source to enter its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// Wait for Ready to be called after the startup barrier completes.
	require.Eventually(t, func() bool {
		for _, c := range readinessExt.getReadyCalls() {
			if c == "ready" {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "Ready must be called after source enters run loop")

	cancel()
	require.NoError(t, <-done)

	// Plain extension must have been started and shut down normally.
	plainExt.mu.Lock()
	assert.Equal(t, 1, plainExt.startCount, "plain extension must be started")
	assert.Equal(t, 1, plainExt.shutdownCount, "plain extension must be shut down")
	plainExt.mu.Unlock()

	// Readiness extension must have received both Ready and NotReady.
	calls := readinessExt.getReadyCalls()
	hasReady := false
	hasNotReady := false
	for _, c := range calls {
		if c == "ready" {
			hasReady = true
		}
		if c == "not_ready" {
			hasNotReady = true
		}
	}
	assert.True(t, hasReady, "readiness extension must have received Ready")
	assert.True(t, hasNotReady, "readiness extension must have received NotReady")
}

func TestRun_StartFailureMarksNotReadyOnStartedReadinessExtensions(t *testing.T) {
	// Extension "aaa-health/ok" (readiness) starts successfully.
	// Extension "zzz-failing/bad" fails to start (sorted after aaa-health).
	// The service must call NotReady on the readiness extension before
	// shutting it down.
	readinessExt := &fakeReadinessExtension{}
	failingExt := &fakeExtension{startErr: errors.New("init failed")}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"aaa-health/ok":   cc("aaa-health/ok"),
		"zzz-failing/bad": cc("zzz-failing/bad"),
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "aaa-health", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
			&fakeExtensionFactory{typeName: "zzz-failing", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return failingExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.Error(t, runErr)

	calls := readinessExt.getReadyCalls()
	// Must have received at least one not_ready call.
	hasNotReady := false
	for _, c := range calls {
		if c == "not_ready" {
			hasNotReady = true
		}
	}
	assert.True(t, hasNotReady, "readiness extension must receive NotReady when a subsequent extension fails to start")
}

func TestRun_MultipleReadinessExtensionsAllSignaled(t *testing.T) {
	readinessA := &fakeReadinessExtension{}
	readinessB := &fakeReadinessExtension{}
	sourceStarted := make(chan struct{})

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"health/a": cc("health/a"),
		"health/b": cc("health/b"),
	}

	extMap := map[string]*fakeReadinessExtension{
		"a": readinessA,
		"b": readinessB,
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "health", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return extMap[settings.ID.Name], nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for the source to enter its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// Wait for both readiness extensions to receive Ready.
	require.Eventually(t, func() bool {
		for _, ext := range extMap {
			hasReady := false
			for _, c := range ext.getReadyCalls() {
				if c == "ready" {
					hasReady = true
					break
				}
			}
			if !hasReady {
				return false
			}
		}
		return true
	}, 2*time.Second, 10*time.Millisecond, "all readiness extensions must receive Ready")

	cancel()
	require.NoError(t, <-done)

	for name, ext := range extMap {
		calls := ext.getReadyCalls()
		hasReady := false
		hasNotReady := false
		for _, c := range calls {
			if c == "ready" {
				hasReady = true
			}
			if c == "not_ready" {
				hasNotReady = true
			}
		}
		assert.True(t, hasReady, "readiness extension %s must have received Ready", name)
		assert.True(t, hasNotReady, "readiness extension %s must have received NotReady", name)
	}
}

// =============================================================================
// Source preflight tests
// =============================================================================

// preflightSource implements both Source and SourcePreflight.
type preflightSource struct {
	started      chan struct{}
	preflightErr error
}

func (s *preflightSource) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

func (s *preflightSource) Preflight(_ context.Context) error {
	return s.preflightErr
}

func TestRun_PreflightCalledBeforeSourceStart(t *testing.T) {
	sourceStarted := make(chan struct{})
	src := &preflightSource{started: sourceStarted}

	svc, err := New(context.Background(), minimalPipelineCfg(), catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// If preflight succeeds, the source should start its run loop.
	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start after successful preflight")
	}

	cancel()
	require.NoError(t, <-done)
}

func TestRun_PreflightFailurePreventsSourceStart(t *testing.T) {
	preflightErr := errors.New("model registry unreachable")
	sourceStarted := make(chan struct{})
	src := &preflightSource{started: sourceStarted, preflightErr: preflightErr}

	svc, err := New(context.Background(), minimalPipelineCfg(), catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.Error(t, runErr)
	require.ErrorIs(t, runErr, preflightErr)
	require.ErrorContains(t, runErr, "preflight failed")

	// Source must not have started.
	select {
	case <-sourceStarted:
		t.Fatal("source started despite preflight failure")
	default:
	}
}

func TestRun_PreflightFailureShutdownsExtensions(t *testing.T) {
	ext := &fakeExtension{}
	preflightErr := errors.New("filter rejected")
	sourceStarted := make(chan struct{})
	src := &preflightSource{started: sourceStarted, preflightErr: preflightErr}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"auth": cc("auth")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return ext, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.Error(t, runErr)
	require.ErrorIs(t, runErr, preflightErr)

	ext.mu.Lock()
	shutdownCount := ext.shutdownCount
	ext.mu.Unlock()
	require.Equal(t, 1, shutdownCount, "extension must be shut down after preflight failure")
}

func TestRun_SourcesWithoutPreflightAreNotAffected(t *testing.T) {
	// A plain blockingSource that does not implement SourcePreflight
	// must still start normally.
	sourceStarted := make(chan struct{})
	svc, err := New(context.Background(), minimalPipelineCfg(), catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	require.NoError(t, <-done)
}

func TestRun_PreflightFailureMarksReadinessNotReady(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	preflightErr := errors.New("filter rejected")
	sourceStarted := make(chan struct{})
	src := &preflightSource{started: sourceStarted, preflightErr: preflightErr}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"healthext": cc("healthext")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, preflightErr)

	calls := readinessExt.getReadyCalls()
	hasNotReady := false
	for _, c := range calls {
		if c == "not_ready" {
			hasNotReady = true
		}
	}
	assert.True(t, hasNotReady, "readiness extension must receive NotReady when preflight fails")
}

func TestRun_ExtensionsStartedBeforePreflight(t *testing.T) {
	var mu sync.Mutex
	var order []string

	ext := &fakeExtension{
		onStart: func(_ catalogcollector.Host) {
			mu.Lock()
			order = append(order, "ext-start")
			mu.Unlock()
		},
	}

	customSrc := &orderTrackingPreflightSource{
		started: make(chan struct{}),
		onPreflight: func() {
			mu.Lock()
			order = append(order, "preflight")
			mu.Unlock()
		},
	}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"auth": cc("auth")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return customSrc, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return ext, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-customSrc.started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	require.NoError(t, <-done)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 2)
	assert.Equal(t, "ext-start", order[0], "extension must start before preflight")
	assert.Equal(t, "preflight", order[1], "preflight must run after extension start")
}

// orderTrackingPreflightSource records preflight calls for ordering assertions.
type orderTrackingPreflightSource struct {
	started     chan struct{}
	onPreflight func()
}

func (s *orderTrackingPreflightSource) Run(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return nil
}

func (s *orderTrackingPreflightSource) Preflight(_ context.Context) error {
	if s.onPreflight != nil {
		s.onPreflight()
	}
	return nil
}

func TestRun_SharedSourcePreflightedOnce(t *testing.T) {
	var preflightCount atomic.Int32
	src := &countingPreflightSource{
		started:     make(chan struct{}),
		onPreflight: func() { preflightCount.Add(1) },
	}

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source/shared": cc("fake-source/shared")},
		Destinations: map[string]config.ComponentConfig{"fake-dest/a": cc("fake-dest/a"), "fake-dest/b": cc("fake-dest/b")},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/shared", Destination: "fake-dest/a"},
			"pipeline-b": {Source: "fake-source/shared", Destination: "fake-dest/b"},
		},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-src.started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, int32(1), preflightCount.Load(), "shared source must be preflighted exactly once")
}

// TestRun_SharedSourceConstructedAndPreflightedOnce pins the invariant that
// makes a separate preflight deduplication pass unnecessary: a source
// referenced by several pipelines is constructed exactly once per source ID,
// so s.sources already holds unique IDs by the time preflight runs.
func TestRun_SharedSourceConstructedAndPreflightedOnce(t *testing.T) {
	var constructCount atomic.Int32
	var preflightCount atomic.Int32

	src := &countingPreflightSource{
		started:     make(chan struct{}),
		onPreflight: func() { preflightCount.Add(1) },
	}

	cfg := &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/shared": cc("fake-source/shared"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest/a": cc("fake-dest/a"),
			"fake-dest/b": cc("fake-dest/b"),
			"fake-dest/c": cc("fake-dest/c"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/shared", Destination: "fake-dest/a"},
			"pipeline-b": {Source: "fake-source/shared", Destination: "fake-dest/b"},
			"pipeline-c": {Source: "fake-source/shared", Destination: "fake-dest/c"},
		},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				constructCount.Add(1)
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	assert.Equal(t, int32(1), constructCount.Load(),
		"a source shared by three pipelines must be constructed exactly once")
	assert.Len(t, svc.sources, 1, "the service must hold one running source per source ID")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-src.started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, int32(1), preflightCount.Load(),
		"a source shared by three pipelines must be preflighted exactly once")
}

// countingPreflightSource implements Source + SourcePreflight with a callback.
type countingPreflightSource struct {
	started     chan struct{}
	onPreflight func()
	closedOnce  sync.Once
}

func (s *countingPreflightSource) Run(ctx context.Context) error {
	s.closedOnce.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil
}

func (s *countingPreflightSource) Preflight(_ context.Context) error {
	if s.onPreflight != nil {
		s.onPreflight()
	}
	return nil
}

func TestRun_PreflightFailureNeverCallsReady(t *testing.T) {
	readinessExt := &fakeReadinessExtension{}
	preflightErr := errors.New("filter rejected")
	src := &preflightSource{started: make(chan struct{}), preflightErr: preflightErr}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{"healthext": cc("healthext")}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "healthext", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, preflightErr)

	calls := readinessExt.getReadyCalls()
	for _, c := range calls {
		assert.NotEqual(t, "ready", c, "Ready must never be called when preflight fails")
	}
}

func TestRun_PreflightFailureStartedExtensionsNotReadyAndShutdownReverse(t *testing.T) {
	var mu sync.Mutex
	var lifecycle []string

	readinessExt := &fakeReadinessExtension{}

	extA := &fakeExtension{
		onShutdown: func() {
			mu.Lock()
			lifecycle = append(lifecycle, "a-shutdown")
			mu.Unlock()
		},
	}
	extB := &fakeExtension{
		onShutdown: func() {
			mu.Lock()
			lifecycle = append(lifecycle, "b-shutdown")
			mu.Unlock()
		},
	}

	preflightErr := errors.New("preflight failed")
	src := &preflightSource{started: make(chan struct{}), preflightErr: preflightErr}

	cfg := minimalPipelineCfg()
	cfg.Extensions = map[string]config.ComponentConfig{
		"aaa-health/ok": cc("aaa-health/ok"),
		"ext/a":         cc("ext/a"),
		"ext/b":         cc("ext/b"),
	}

	extMap := map[string]*fakeExtension{"a": extA, "b": extB}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&readinessExtensionFactory{typeName: "aaa-health", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return readinessExt, nil
			}},
			&fakeExtensionFactory{typeName: "ext", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				return extMap[settings.ID.Name], nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.ErrorIs(t, runErr, preflightErr)

	// Readiness extension must have NotReady called.
	hasNotReady := false
	for _, c := range readinessExt.getReadyCalls() {
		if c == "not_ready" {
			hasNotReady = true
		}
	}
	assert.True(t, hasNotReady, "readiness extension must receive NotReady on preflight failure")

	// Extensions must be shut down in reverse order.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, lifecycle, 2)
	assert.Equal(t, "b-shutdown", lifecycle[0], "ext/b must be shut down before ext/a (reverse order)")
	assert.Equal(t, "a-shutdown", lifecycle[1])
}

func TestDestinationConsumer_ProcessorGeneratedInvalidSnapshot_DestinationNotCalled(t *testing.T) {
	dst := &recordingConsumer{}
	// Build a pipeline with a processor that strips the revision (making snapshot invalid).
	var head catalogcollector.Consumer

	_, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc": cc("fake-proc")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines: map[string]config.PipelineConfig{
			"p": {Source: "fake-source", Processors: []string{"fake-proc"}, Destination: "fake-dest"},
		},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				head = next
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				return &revisionStrippingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return dst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.NotNil(t, head)

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "valid-revision",
		Catalogs: []apiv1alpha1.Catalog{
			{Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("cat")}},
		},
	}
	err = head.Consume(context.Background(), snap)
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot validation failed")
	require.Empty(t, dst.snapshots, "destination must not be called for invalid snapshot")
}

// revisionStrippingProcessor clears the revision, making the snapshot invalid.
type revisionStrippingProcessor struct {
	next catalogcollector.Consumer
}

func (p *revisionStrippingProcessor) Consume(ctx context.Context, s *catalogcollector.CatalogSnapshot) error {
	// Make a copy with empty revision.
	stripped := &catalogcollector.CatalogSnapshot{
		Revision:     "",
		Catalogs:     s.Catalogs,
		CatalogItems: s.CatalogItems,
	}
	return p.next.Consume(ctx, stripped)
}

func TestDestinationConsumer_InvalidSnapshotWithoutProcessors_Rejected(t *testing.T) {
	dst := &recordingConsumer{}
	consumer := &destinationConsumer{
		pipelineID:  "test-pipeline",
		destination: dst,
	}

	// Snapshot with empty revision and no catalogs is invalid.
	snap := &catalogcollector.CatalogSnapshot{Revision: ""}
	err := consumer.Consume(context.Background(), snap)
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot validation failed")
	require.Empty(t, dst.snapshots)
}

// sourceState holds mutable counters behind a comparable pointer so that
// nonComparablePreflightSource can use value receivers and be stored as a
// struct value (not a pointer) in the Service.sources slice.
type sourceState struct {
	started        chan struct{}
	preflightCalls atomic.Int32
	runCalls       atomic.Int32
	closedOnce     sync.Once
}

// nonComparablePreflightSource implements Source and SourcePreflight.
// The struct VALUE is non-comparable (contains a slice), but mutable
// counters are behind the comparable *sourceState pointer. Value receivers
// ensure the factory returns an interface wrapping the struct value.
type nonComparablePreflightSource struct {
	tags  []string     // makes the struct non-comparable
	state *sourceState // comparable pointer for mutable counters
}

func (s nonComparablePreflightSource) Run(ctx context.Context) error {
	s.state.runCalls.Add(1)
	s.state.closedOnce.Do(func() { close(s.state.started) })
	<-ctx.Done()
	return nil
}

func (s nonComparablePreflightSource) Preflight(_ context.Context) error {
	s.state.preflightCalls.Add(1)
	return nil
}

func TestRun_NonComparableSourcePreflight(t *testing.T) {
	state := &sourceState{started: make(chan struct{})}
	// Return the struct VALUE (not a pointer) so the interface holds a
	// genuinely non-comparable value.
	src := nonComparablePreflightSource{
		tags:  []string{"non", "comparable"},
		state: state,
	}

	svc, err := New(context.Background(), minimalPipelineCfg(), catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return src, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	select {
	case <-state.started:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	cancel()
	require.NoError(t, <-done)

	assert.Equal(t, int32(1), state.preflightCalls.Load(), "preflight must be called once")
	assert.Equal(t, int32(1), state.runCalls.Load(), "run must be called once")
}

func TestRun_MultiSourcePreflightOrderAndCompletion(t *testing.T) {
	// Verify that with ≥2 sources implementing SourcePreflight:
	// 1. All preflights are called in deterministic (sorted) order.
	// 2. Each source's Run callback can observe that BOTH preflights completed.
	// 3. Preflight and Run are called exactly once per source.
	var mu sync.Mutex
	var preflightOrder []string
	preflightDoneA := make(chan struct{})
	preflightDoneB := make(chan struct{})

	srcA := &orderVerifyingPreflightSource{
		name:    "a-src",
		started: make(chan struct{}),
		onPreflight: func() {
			mu.Lock()
			preflightOrder = append(preflightOrder, "a-src")
			mu.Unlock()
			close(preflightDoneA)
		},
		preflightDoneAll: [2]<-chan struct{}{preflightDoneA, preflightDoneB},
	}
	srcB := &orderVerifyingPreflightSource{
		name:    "b-src",
		started: make(chan struct{}),
		onPreflight: func() {
			mu.Lock()
			preflightOrder = append(preflightOrder, "b-src")
			mu.Unlock()
			close(preflightDoneB)
		},
		preflightDoneAll: [2]<-chan struct{}{preflightDoneA, preflightDoneB},
	}

	srcMap := map[string]*orderVerifyingPreflightSource{
		"a-src": srcA,
		"b-src": srcB,
	}

	cfg := &config.Config{
		Sources: map[string]config.ComponentConfig{
			"fake-source/a-src": cc("fake-source/a-src"),
			"fake-source/b-src": cc("fake-source/b-src"),
		},
		Destinations: map[string]config.ComponentConfig{
			"fake-dest": cc("fake-dest"),
		},
		Pipelines: map[string]config.PipelineConfig{
			"pipeline-a": {Source: "fake-source/a-src", Destination: "fake-dest"},
			"pipeline-b": {Source: "fake-source/b-src", Destination: "fake-dest"},
		},
	}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return srcMap[settings.ID.Name], nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()

	// Wait for both sources to start their run loops.
	for _, src := range srcMap {
		select {
		case <-src.started:
		case <-time.After(2 * time.Second):
			t.Fatalf("source %s did not start", src.name)
		}
	}

	cancel()
	require.NoError(t, <-done)

	// Assert deterministic preflight order (sorted by component ID string).
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"a-src", "b-src"}, preflightOrder,
		"preflights must run in deterministic sorted order")

	// Assert each source's Run confirmed both preflights completed.
	for _, src := range srcMap {
		assert.True(t, src.runSawAllPreflights,
			"source %s: Run must observe all preflights complete", src.name)
		assert.Equal(t, int32(1), src.preflightCalls.Load(),
			"source %s: Preflight must be called exactly once", src.name)
		assert.Equal(t, int32(1), src.runCalls.Load(),
			"source %s: Run must be called exactly once", src.name)
	}
}

// orderVerifyingPreflightSource verifies that when Run is called, all
// preflights (for all sources) have already completed.
type orderVerifyingPreflightSource struct {
	name                string
	started             chan struct{}
	onPreflight         func()
	preflightDoneAll    [2]<-chan struct{} // channels closed by each source's preflight
	runSawAllPreflights bool
	preflightCalls      atomic.Int32
	runCalls            atomic.Int32
	closedOnce          sync.Once
}

func (s *orderVerifyingPreflightSource) Run(ctx context.Context) error {
	s.runCalls.Add(1)
	// Verify all preflight channels are already closed (non-blocking).
	allDone := true
	for _, ch := range s.preflightDoneAll {
		select {
		case <-ch:
		default:
			allDone = false
		}
	}
	s.runSawAllPreflights = allDone
	s.closedOnce.Do(func() { close(s.started) })
	<-ctx.Done()
	return nil
}

func (s *orderVerifyingPreflightSource) Preflight(_ context.Context) error {
	s.preflightCalls.Add(1)
	if s.onPreflight != nil {
		s.onPreflight()
	}
	return nil
}

// =============================================================================
// Snapshot validation tests
// =============================================================================

func TestDestinationConsumer_ValidSnapshot(t *testing.T) {
	dst := &recordingConsumer{}
	consumer := &destinationConsumer{
		pipelineID:  "test-pipeline",
		destination: dst,
	}

	snap := &catalogcollector.CatalogSnapshot{
		Revision: "abc123",
		Catalogs: []apiv1alpha1.Catalog{
			{Metadata: apiv1beta1.ObjectMeta{Name: lo.ToPtr("my-catalog")}},
		},
	}

	err := consumer.Consume(context.Background(), snap)
	require.NoError(t, err)
	require.Len(t, dst.snapshots, 1)
}

func TestDestinationConsumer_NilSnapshot(t *testing.T) {
	consumer := &destinationConsumer{
		pipelineID:  "test-pipeline",
		destination: &recordingConsumer{},
	}

	err := consumer.Consume(context.Background(), nil)
	require.Error(t, err)
	require.ErrorContains(t, err, "snapshot validation failed")
}

func TestRun_NilSourceSnapshot_HandledWithoutPanic(t *testing.T) {
	dst := &recordingConsumer{}
	var head catalogcollector.Consumer

	svc, err := New(context.Background(), &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Destination: "fake-dest"}},
	}, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Source, error) {
				head = next
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return dst, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)
	require.NotNil(t, svc)
	require.NotNil(t, head)

	// Pass nil through the instrumented source fanout consumer.
	err = head.Consume(context.Background(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "source emitted nil snapshot")

	// Destination must not have been called.
	require.Empty(t, dst.snapshots, "destination must not be called for nil snapshot")
}

func TestDestinationConsumer_EmptyRevision(t *testing.T) {
	consumer := &destinationConsumer{
		pipelineID:  "test-pipeline",
		destination: &recordingConsumer{},
	}

	snap := &catalogcollector.CatalogSnapshot{Revision: ""}
	err := consumer.Consume(context.Background(), snap)
	require.Error(t, err)
	require.ErrorContains(t, err, "snapshot validation failed")
}

// =============================================================================
// Metrics integration tests
// =============================================================================

func TestNew_FactoryReceivesMeterProvider(t *testing.T) {
	var srcMP, dstMP, procMP, extMP metric.MeterProvider

	cfg := &config.Config{
		Sources:      map[string]config.ComponentConfig{"fake-source": cc("fake-source")},
		Processors:   map[string]config.ComponentConfig{"fake-proc": cc("fake-proc")},
		Destinations: map[string]config.ComponentConfig{"fake-dest": cc("fake-dest")},
		Extensions:   map[string]config.ComponentConfig{"auth": cc("auth")},
		Pipelines:    map[string]config.PipelineConfig{"p": {Source: "fake-source", Processors: []string{"fake-proc"}, Destination: "fake-dest"}},
	}

	_, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcMP = settings.MeterProvider
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Processors: []catalogcollector.ProcessorFactory{
			&fakeProcessorFactory{typeName: "fake-proc", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, next catalogcollector.Consumer) (catalogcollector.Consumer, error) {
				procMP = settings.MeterProvider
				return &recordingProcessor{next: next}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				dstMP = settings.MeterProvider
				return &recordingConsumer{}, nil
			}},
		},
		Extensions: []catalogcollector.ExtensionFactory{
			&fakeExtensionFactory{typeName: "auth", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Extension, error) {
				extMP = settings.MeterProvider
				return &fakeExtension{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	// All components must receive the same non-nil MeterProvider.
	require.NotNil(t, srcMP, "source must receive a MeterProvider")
	require.NotNil(t, dstMP, "destination must receive a MeterProvider")
	require.NotNil(t, procMP, "processor must receive a MeterProvider")
	require.NotNil(t, extMP, "extension must receive a MeterProvider")
	// When metrics are disabled (nil config), noop.MeterProvider is a value
	// type that cannot be compared with assert.Same. Use equality instead.
	assert.Equal(t, srcMP, dstMP, "all components must receive the same MeterProvider")
	assert.Equal(t, srcMP, procMP)
	assert.Equal(t, srcMP, extMP)
}

func TestNew_DisabledMetricsUsesNoopMeterProvider(t *testing.T) {
	var srcMP metric.MeterProvider

	cfg := minimalPipelineCfg()
	// No metrics config → disabled.

	_, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, settings catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				srcMP = settings.MeterProvider
				return &blockingSource{started: make(chan struct{})}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	require.NotNil(t, srcMP)
	_, isNoop := srcMP.(noop.MeterProvider)
	assert.True(t, isNoop, "disabled metrics must pass noop.MeterProvider to components")
}

func TestNew_ConstructionFailureDoesNotRetainMetricsPort(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := probe.Addr().String()
	require.NoError(t, probe.Close())

	cfg := minimalPipelineCfg()
	cfg.Service.Metrics = &config.MetricsConfig{Endpoint: address}

	// No factories are registered, so construction must fail after the meter
	// provider has been created.
	_, err = New(
		context.Background(),
		cfg,
		catalogcollector.Factories{},
		testSettings(),
	)
	require.Error(t, err)

	listener, listenErr := net.Listen("tcp", address)
	require.NoError(
		t,
		listenErr,
		"failed service construction must not retain the metrics port",
	)
	require.NoError(t, listener.Close())
}

func TestRun_MetricsBindFailurePreventsSourceStart(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() {
		require.NoError(t, occupied.Close())
	}()

	sourceStarted := make(chan struct{})
	cfg := minimalPipelineCfg()
	cfg.Service.Metrics = &config.MetricsConfig{Endpoint: occupied.Addr().String()}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	runErr := svc.Run(context.Background())
	require.Error(t, runErr)
	require.ErrorContains(t, runErr, "starting metrics endpoint")

	select {
	case <-sourceStarted:
		t.Fatal("source started despite metrics bind failure")
	default:
	}
}

func TestRun_UnexpectedMetricsServerExitStopsSources(t *testing.T) {
	sourceStarted := make(chan struct{})
	cfg := minimalPipelineCfg()
	cfg.Service.Metrics = &config.MetricsConfig{Endpoint: "127.0.0.1:0"}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- svc.Run(context.Background())
	}()

	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}

	// Simulate an unexpected listener failure without invoking the runtime's
	// normal Shutdown path.
	require.NoError(t, svc.metrics.listener.Close())

	select {
	case runErr := <-done:
		require.Error(t, runErr)
		require.ErrorContains(t, runErr, "metrics endpoint stopped unexpectedly")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after metrics endpoint failure")
	}
}

func TestRun_CallerCancellationWithMetricsIsGraceful(t *testing.T) {
	sourceStarted := make(chan struct{})
	cfg := minimalPipelineCfg()
	cfg.Service.Metrics = &config.MetricsConfig{Endpoint: "127.0.0.1:0"}

	svc, err := New(context.Background(), cfg, catalogcollector.Factories{
		Sources: []catalogcollector.SourceFactory{
			&fakeSourceFactory{typeName: "fake-source", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig, _ catalogcollector.Consumer) (catalogcollector.Source, error) {
				return &blockingSource{started: sourceStarted}, nil
			}},
		},
		Destinations: []catalogcollector.DestinationFactory{
			&fakeDestinationFactory{typeName: "fake-dest", createFunc: func(_ context.Context, _ catalogcollector.Settings, _ catalogcollector.ComponentConfig) (catalogcollector.Destination, error) {
				return &recordingConsumer{}, nil
			}},
		},
	}, testSettings())
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- svc.Run(ctx)
	}()

	select {
	case <-sourceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("source did not start")
	}
	cancel()

	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after caller cancellation")
	}
}
