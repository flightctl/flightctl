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
	"github.com/samber/lo"
)

const (
	AppTypeLabel            = "appType"
	DefaultImageManifestDir = "/"
)

type imageProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	handler    appTypeHandler
	spec       *ApplicationSpec

	// AppData stores the extracted app data from OCITargets to reuse in Verify
	AppData *AppData
}

func newImageHandler(appType v1alpha1.AppType, name string, rw fileio.ReadWriter, l *log.PrefixLogger, vm VolumeManager) (appTypeHandler, error) {
	switch appType {
	case v1alpha1.AppTypeQuadlet:
		qb := &quadletBehavior{
			name: name,
			rw:   rw,
		}
		qb.volumeProvider = func() ([]*Volume, error) {
			return extractQuadletVolumesFromDir(qb.ID(), rw, qb.AppPath())
		}
		return qb, nil
	case v1alpha1.AppTypeCompose:
		return &composeBehavior{
			name: name,
			rw:   rw,
			log:  l,
			vm:   vm,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newImage(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter, appType v1alpha1.AppType) (*imageProvider, error) {
	provider, err := spec.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}

	// set the app name to the image name if not provided
	appName := lo.FromPtr(spec.Name)
	if appName == "" {
		appName = provider.Image
	}

	volumeManager, err := NewVolumeManager(log, appName, provider.Volumes)
	if err != nil {
		return nil, err
	}

	handler, err := newImageHandler(appType, appName, readWriter, log, volumeManager)
	if err != nil {
		return nil, fmt.Errorf("constructing image handler: %w", err)
	}

	return &imageProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		handler:    handler,
		spec: &ApplicationSpec{
			Name:          appName,
			ID:            handler.ID(),
			AppType:       appType,
			Path:          handler.AppPath(),
			EnvVars:       lo.FromPtr(spec.EnvVars),
			Embedded:      false,
			ImageProvider: &provider,
			Volume:        volumeManager,
		},
	}, nil
}

func (p *imageProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	if p.spec.AppType != v1alpha1.AppTypeCompose && p.spec.AppType != v1alpha1.AppTypeQuadlet {
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}

	if err := ensureDependenciesFromAppType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ImageProvider.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	var tmpAppPath string
	var shouldCleanup bool

	if p.AppData != nil {
		tmpAppPath = p.AppData.TmpPath
		shouldCleanup = false
	} else {
		// no cache, extract the image contents
		var err error
		tmpAppPath, err = p.readWriter.MkdirTemp("app_temp")
		if err != nil {
			return fmt.Errorf("creating tmp dir: %w", err)
		}
		shouldCleanup = true

		// copy image contents to a tmp directory for further processing
		if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, tmpAppPath); err != nil {
			if rmErr := p.readWriter.RemoveAll(tmpAppPath); rmErr != nil {
				p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, rmErr)
			}
			return fmt.Errorf("copy image contents: %w", err)
		}
	}

	defer func() {
		if shouldCleanup && tmpAppPath != "" {
			if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
				p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, err)
			}
			p.AppData = nil
		}
	}()

	return p.handler.Verify(ctx, tmpAppPath)
}

func (p *imageProvider) Install(ctx context.Context) error {
	// cleanup any cached extracted path from OCITargets since Install will extract to final location
	if p.AppData != nil {
		p.log.Debugf("Cleaning up cached app data before Install")
		if cleanupErr := p.AppData.Cleanup(); cleanupErr != nil {
			p.log.Warnf("Failed to cleanup cached app data: %v", cleanupErr)
		}
		p.AppData = nil
	}

	if p.spec.ImageProvider == nil {
		return fmt.Errorf("image application spec is nil")
	}

	if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, p.spec.Path); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	// image providers may have volumes that are nested within the contents of the application
	// that can't be added until install time
	volumes, err := p.handler.Volumes()
	if err != nil {
		return fmt.Errorf("getting volumes: %w", err)
	}
	p.spec.Volume.AddVolumes(p.spec.Name, volumes)

	return p.handler.Install(ctx)
}

func (p *imageProvider) Remove(ctx context.Context) error {
	// cleanup any cached extracted path
	if p.AppData != nil {
		p.log.Debugf("Cleaning up cached app data before Remove")
		if cleanupErr := p.AppData.Cleanup(); cleanupErr != nil {
			p.log.Warnf("Failed to cleanup cached app data: %v", cleanupErr)
		}
		p.AppData = nil
	}

	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return p.handler.Remove(ctx)
}

func (p *imageProvider) Name() string {
	return p.spec.Name
}

func (p *imageProvider) Spec() *ApplicationSpec {
	return p.spec
}

// typeFromImage returns the app type from the image label take from the image in local container storage.
func typeFromImage(ctx context.Context, podman *client.Podman, image string) (v1alpha1.AppType, error) {
	labels, err := podman.InspectLabels(ctx, image)
	if err != nil {
		return "", err
	}
	appTypeLabel, ok := labels[AppTypeLabel]
	if !ok {
		return "", fmt.Errorf("%w: %s, %s", errors.ErrAppLabel, AppTypeLabel, image)
	}
	appType := v1alpha1.AppType(appTypeLabel)
	if appType == "" {
		return "", fmt.Errorf("%w: %s", errors.ErrParseAppType, appTypeLabel)
	}
	return appType, nil
}

var _ appTypeHandler = (*quadletBehavior)(nil)
var _ appTypeHandler = (*composeBehavior)(nil)

type quadletBehavior struct {
	name           string
	rw             fileio.ReadWriter
	volumeProvider volumeProvider
}

func (b *quadletBehavior) Verify(ctx context.Context, path string) error {
	return ensureQuadlet(b.rw, path)
}

func (b *quadletBehavior) Install(ctx context.Context) error {
	if err := installQuadlet(b.rw, b.AppPath(), b.ID()); err != nil {
		return fmt.Errorf("installing quadlet: %w", err)
	}
	return nil
}

func (b *quadletBehavior) Remove(ctx context.Context) error {
	return nil
}

func (b *quadletBehavior) AppPath() string {
	return filepath.Join(lifecycle.QuadletAppPath, b.name)
}

func (b *quadletBehavior) ID() string {
	return client.NewComposeID(b.name)
}

func (b *quadletBehavior) Volumes() ([]*Volume, error) {
	return b.volumeProvider()
}

type composeBehavior struct {
	name string
	rw   fileio.ReadWriter
	log  *log.PrefixLogger
	vm   VolumeManager
}

func (b *composeBehavior) Verify(ctx context.Context, path string) error {
	if err := ensureCompose(b.rw, path); err != nil {
		return fmt.Errorf("ensuring compose: %w", err)
	}
	return nil
}

func (b *composeBehavior) Install(ctx context.Context) error {
	if err := writeComposeOverride(b.log, b.AppPath(), b.vm, b.rw, client.ComposeOverrideFilename); err != nil {
		return fmt.Errorf("writing override file %w", err)
	}
	return nil
}

func (b *composeBehavior) Remove(ctx context.Context) error {
	return nil
}

func (b *composeBehavior) AppPath() string {
	return filepath.Join(lifecycle.ComposeAppPath, b.name)
}

func (b *composeBehavior) ID() string {
	return client.NewComposeID(b.name)
}

func (b *composeBehavior) Volumes() ([]*Volume, error) {
	return nil, nil
}
