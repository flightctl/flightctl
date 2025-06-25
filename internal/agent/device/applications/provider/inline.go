package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
}

func newInline(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter) (*inlineProvider, error) {
	provider, err := spec.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}

	appName := lo.FromPtr(spec.Name)
	volumeManager, err := NewVolumeManager(log, appName, provider.Volumes)
	if err != nil {
		return nil, err
	}

	p := &inlineProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:           appName,
			AppType:        lo.FromPtr(spec.AppType),
			EnvVars:        lo.FromPtr(spec.EnvVars),
			Embedded:       false,
			InlineProvider: &provider,
			Volume:         volumeManager,
		},
	}

	path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
	if err != nil {
		return nil, fmt.Errorf("getting app path: %w", err)
	}

	p.spec.Path = path
	p.spec.ID = client.NewComposeID(p.spec.Name)

	return p, nil

}

func (p *inlineProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}
	if err := ensureDependenciesFromAppType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring app dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.InlineProvider.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	// create a temporary directory to copy the image contents
	tmpAppPath, err := p.readWriter.MkdirTemp("app_temp")
	if err != nil {
		return fmt.Errorf("creating tmp dir: %w", err)
	}

	cleanup := func() {
		if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
			p.log.Errorf("Cleaning up temporary directory %q: %v", tmpAppPath, err)
		}
	}
	defer cleanup()

	// copy image contents to a tmp directory for further processing
	if err := p.writeInlineContent(tmpAppPath, p.spec.InlineProvider.Inline); err != nil {
		return err
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
		if err := ensureVolumesContent(ctx, p.log, p.podman, p.spec.InlineProvider.Volumes); err != nil {
			return fmt.Errorf("ensuring volumes: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
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

	if err := writeComposeOverride(p.log, p.spec.Path, p.spec.Volume, p.readWriter, client.ComposeOverrideFilename); err != nil {
		return fmt.Errorf("writing override file %w", err)
	}

	return nil
}

func (p *inlineProvider) writeInlineContent(appPath string, contents []v1alpha1.ApplicationContent) error {
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
	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
}

func (p *inlineProvider) Name() string {
	return p.spec.Name
}

func (p *inlineProvider) Spec() *ApplicationSpec {
	return p.spec
}
