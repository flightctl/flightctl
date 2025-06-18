package provider

import (
	"context"
	"fmt"

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
	spec       *ApplicationSpec
}

func newImage(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter) (*imageProvider, error) {
	provider, err := spec.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("getting provider spec:%w", err)
	}

	// set the app name to the image name if not provided
	appName := lo.FromPtr(spec.Name)
	if appName == "" {
		appName = provider.Image
	}
	embedded := false
	path, err := pathFromAppType(v1alpha1.AppTypeCompose, appName, embedded)
	if err != nil {
		return nil, fmt.Errorf("getting app path: %w", err)
	}

	return &imageProvider{
		log:        log,
		podman:     podman,
		readWriter: readWriter,
		spec: &ApplicationSpec{
			Name:          appName,
			AppType:       lo.FromPtr(spec.AppType),
			Path:          path,
			EnvVars:       lo.FromPtr(spec.EnvVars),
			Embedded:      embedded,
			ImageProvider: &provider,
			Volumes:       provider.Volumes,
		},
	}, nil
}

func (p *imageProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	image := p.spec.ImageProvider.Image
	if err := ensureImageExists(ctx, p.log, p.podman, image, v1alpha1.PullIfNotPresent); err != nil {
		return fmt.Errorf("pulling image: %w", err)
	}

	// type declared in the spec overrides the type from the image
	if p.spec.AppType == "" {
		appType, err := typeFromImage(ctx, p.podman, image)
		if err != nil {
			return fmt.Errorf("getting app type: %w", err)
		}
		p.spec.AppType = appType
	}

	if err := ensureDependenciesFromAppType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ImageProvider.Volumes); err != nil {
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
	if err := p.podman.CopyContainerData(ctx, image, tmpAppPath); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		p.spec.ID = lifecycle.NewComposeID(p.spec.Name)
		path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
		if err != nil {
			return fmt.Errorf("getting app path: %w", err)
		}
		p.spec.Path = path

		// ensure the compose application content in tmp dir is valid
		if err := ensureCompose(ctx, p.log, p.podman, p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
		if err := ensureVolumesContent(ctx, p.log, p.podman, p.spec.ImageProvider.Volumes); err != nil {
			return fmt.Errorf("ensuring volumes: %w", err)
		}
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}

	return nil
}

func (p *imageProvider) Install(ctx context.Context) error {
	if p.spec.ImageProvider == nil {
		return fmt.Errorf("image application spec is nil")
	}

	if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, p.spec.Path); err != nil {
		return fmt.Errorf("copy image contents: %w", err)
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	labels := []string{fmt.Sprintf("com.docker.compose.project=%s", p.spec.ID)}
	if err := ensurePodmanVolumes(ctx, p.log, p.readWriter, p.podman, p.spec.ImageProvider.Volumes, labels); err != nil {
		return fmt.Errorf("creating volumes: %w", err)
	}

	spec, err := client.ParseComposeSpecFromDir(p.readWriter, p.spec.Path)
	if err != nil {
		p.log.WithError(err).Errorf("Failed to parse Compose spec from path: %q", p.spec.Path)
		return err
	}

	p.log.Debugf("Successfully parsed Compose spec from %q with %d services and %d volumes",
		p.spec.Path, len(spec.Services), len(spec.Volumes))

	patched, renamed := patchRenamedVolumesInComposeSpec(p.log, spec, p.spec.Volumes)

	if len(renamed) == 0 {
		p.log.Debug("No volume names matched for renaming; skipping override file generation.")
	} else {
		p.log.Debugf("Volumes renamed: %v", renamed)
	}

	if err := writeComposeOverrideDiff(p.log, p.spec.Path, spec, patched, renamed, p.readWriter, client.ComposeOverrideFilename); err != nil {
		p.log.WithError(err).Errorf("Failed to write Compose override file to %q", client.ComposeOverrideFilename)
		return err
	}
	p.log.Debugf("Compose override diff written (if any changes) to %q", client.ComposeOverrideFilename)

	return nil
}

func (p *imageProvider) Remove(ctx context.Context) error {
	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing application: %w", err)
	}
	return nil
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
