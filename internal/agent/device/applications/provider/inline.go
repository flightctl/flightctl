package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type inlineProvider struct {
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	spec       *ApplicationSpec
	handler    appTypeHandler
}

func newInlineHandler(appType v1beta1.AppType, name string, rw fileio.ReadWriter, spec *v1beta1.InlineApplicationProviderSpec, l *log.PrefixLogger, vm VolumeManager) (appTypeHandler, error) {
	switch appType {
	case v1beta1.AppTypeQuadlet:
		qb := &quadletHandler{
			name:        name,
			rw:          rw,
			log:         l,
			specVolumes: lo.FromPtr(spec.Volumes),
		}
		qb.volumeProvider = func() ([]*Volume, error) {
			return extractQuadletVolumesFromSpec(qb.ID(), spec.Inline)
		}
		return qb, nil
	case v1beta1.AppTypeCompose:
		return &composeHandler{
			name:        name,
			rw:          rw,
			log:         l,
			vm:          vm,
			specVolumes: lo.FromPtr(spec.Volumes),
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newInline(log *log.PrefixLogger, podman *client.Podman, spec *v1beta1.ApplicationProviderSpec, readWriter fileio.ReadWriter) (*inlineProvider, error) {
	provider, err := spec.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}
	appName := lo.FromPtr(spec.Name)
	appType := spec.AppType
	volumeManager, err := NewVolumeManager(log, appName, appType, provider.Volumes)
	if err != nil {
		return nil, err
	}

	handler, err := newInlineHandler(appType, appName, readWriter, &provider, log, volumeManager)
	if err != nil {
		return nil, fmt.Errorf("constructing inline app handler: %w", err)
	}
	volumes, err := handler.Volumes()
	if err != nil {
		return nil, fmt.Errorf("getting volumes: %w", err)
	}

	volumeManager.AddVolumes(volumes)

	return &inlineProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		handler:    handler,
		spec: &ApplicationSpec{
			Name:           appName,
			AppType:        appType,
			EnvVars:        lo.FromPtr(spec.EnvVars),
			Embedded:       false,
			InlineProvider: &provider,
			Volume:         volumeManager,
			Path:           handler.AppPath(),
			ID:             handler.ID(),
		},
	}, nil
}

func (p *inlineProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}
	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.InlineProvider.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	// create a temporary directory to copy the image contents
	tmpAppPath, err := p.readWriter.MkdirTemp("app_temp")
	if err != nil {
		return fmt.Errorf("creating tmp dir: %w", err)
	}
	defer func() {
		if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
			p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, err)
		}
	}()

	// copy image contents to a tmp directory for further processing
	if err := p.writeInlineContent(tmpAppPath, p.spec.InlineProvider.Inline); err != nil {
		return err
	}

	if err := p.handler.Verify(ctx, tmpAppPath); err != nil {
		return fmt.Errorf("%w: verifying inline: %w", errors.ErrNoRetry, err)
	}
	return nil
}

func (p *inlineProvider) Install(ctx context.Context) error {
	if err := p.writeInlineContent(p.spec.Path, p.spec.InlineProvider.Inline); err != nil {
		return err
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	return p.handler.Install(ctx)
}

func (p *inlineProvider) writeInlineContent(appPath string, contents []v1beta1.ApplicationContent) error {
	if err := p.readWriter.MkdirAll(appPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	for _, content := range contents {
		contentBytes, err := fileio.DecodeContent(lo.FromPtr(content.Content), content.ContentEncoding)
		if err != nil {
			return fmt.Errorf("decoding application content: %w", err)
		}
		contentPath := content.Path
		if len(contentPath) == 0 {
			return fmt.Errorf("application content path is empty")
		}
		if err := p.readWriter.WriteFile(filepath.Join(appPath, contentPath), contentBytes, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("writing application content: %w", err)
		}
	}
	return nil
}

func (p *inlineProvider) Remove(ctx context.Context) error {
	if err := p.readWriter.RemoveAll(p.handler.AppPath()); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return p.handler.Remove(ctx)
}

func (p *inlineProvider) Name() string {
	return p.spec.Name
}

func (p *inlineProvider) Spec() *ApplicationSpec {
	return p.spec
}
