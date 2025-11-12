package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

type embeddedProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
	handler    appTypeHandler
}

func newEmbeddedHandler(appType v1alpha1.AppType, name string, rw fileio.ReadWriter) (appTypeHandler, error) {
	switch appType {
	case v1alpha1.AppTypeQuadlet:
		return &embeddedQuadletBehavior{
			name: name,
			rw:   rw,
		}, nil
	case v1alpha1.AppTypeCompose:
		return &embeddedComposeBehavior{
			name: name,
			rw:   rw,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newEmbedded(log *log.PrefixLogger, podman *client.Podman, readWriter fileio.ReadWriter, name string, appType v1alpha1.AppType) (Provider, error) {
	handler, err := newEmbeddedHandler(appType, name, readWriter)
	if err != nil {
		return nil, fmt.Errorf("constructing embedded app handler: %w", err)
	}

	volumeManager, err := NewVolumeManager(log, name, nil)
	if err != nil {
		return nil, err
	}

	volumes, err := handler.Volumes()
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	volumeManager.AddVolumes(name, volumes)

	return &embeddedProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		handler:    handler,
		spec: &ApplicationSpec{
			Name:     name,
			ID:       handler.ID(),
			AppType:  appType,
			Embedded: true,
			EnvVars:  make(map[string]string),
			Volume:   volumeManager,
			Path:     handler.AppPath(),
		},
	}, nil
}

func (p *embeddedProvider) Verify(ctx context.Context) error {
	return p.handler.Verify(ctx, "")
}

func (p *embeddedProvider) Name() string {
	return p.spec.Name
}

func (p *embeddedProvider) Spec() *ApplicationSpec {
	return p.spec
}

func (p *embeddedProvider) Install(ctx context.Context) error {
	return p.handler.Install(ctx)
}

func (p *embeddedProvider) Remove(ctx context.Context) error {
	return p.handler.Remove(ctx)
}

var _ appTypeHandler = (*embeddedQuadletBehavior)(nil)
var _ appTypeHandler = (*embeddedComposeBehavior)(nil)

type embeddedQuadletBehavior struct {
	name string
	rw   fileio.ReadWriter
}

func (e *embeddedQuadletBehavior) Verify(ctx context.Context, _ string) error {
	return ensureQuadlet(e.rw, e.embeddedPath())
}

func (e *embeddedQuadletBehavior) Install(ctx context.Context) error {
	// quadlet apps must be moved from their embedded location into the default
	// systemd location. A symlink can't be used as the installed contents must be mutated
	// to abide by flightctl's namespacing rules
	if err := e.rw.CopyDir(e.embeddedPath(), e.AppPath(), fileio.WithFollowSymlinkWithinRoot()); err != nil {
		return fmt.Errorf("copying embedded directory to real path: %w", err)
	}

	if err := installQuadlet(e.rw, e.AppPath(), e.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) Remove(ctx context.Context) error {
	if err := e.rw.RemoveAll(e.AppPath()); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
}

func (e *embeddedQuadletBehavior) AppPath() string {
	return filepath.Join(lifecycle.QuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) embeddedPath() string {
	return filepath.Join(lifecycle.EmbeddedQuadletAppPath, e.name)
}

func (e *embeddedQuadletBehavior) ID() string {
	return client.NewComposeID(e.name)
}
func (e *embeddedQuadletBehavior) Volumes() ([]*Volume, error) {
	return extractQuadletVolumesFromDir(e.ID(), e.rw, e.embeddedPath())
}

type embeddedComposeBehavior struct {
	name string
	rw   fileio.ReadWriter
}

func (e *embeddedComposeBehavior) Verify(ctx context.Context, _ string) error {
	if err := ensureCompose(e.rw, e.AppPath()); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}
	return nil
}

func (e *embeddedComposeBehavior) Install(ctx context.Context) error {
	// no-op
	return nil
}

func (e *embeddedComposeBehavior) Remove(ctx context.Context) error {
	return nil
}

func (e *embeddedComposeBehavior) AppPath() string {
	return filepath.Join(lifecycle.EmbeddedComposeAppPath, e.name)
}

func (e *embeddedComposeBehavior) ID() string {
	return client.NewComposeID(e.name)
}
func (e *embeddedComposeBehavior) Volumes() ([]*Volume, error) {
	return nil, nil
}
