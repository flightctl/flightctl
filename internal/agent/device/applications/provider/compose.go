package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

type composeProvider struct {
	log        *log.PrefixLogger
	podman     *client.Podman
	readWriter fileio.ReadWriter
	spec       *ApplicationSpec

	imageRef      string
	inlineContent []v1beta1.ApplicationContent

	AppData *AppData
}

func newComposeProvider(
	ctx context.Context,
	log *log.PrefixLogger,
	podmanFactory client.PodmanFactory,
	apiSpec *v1beta1.ApplicationProviderSpec,
	rwFactory fileio.ReadWriterFactory,
	cfg *parseConfig,
) (*composeProvider, error) {
	composeApp, err := (*apiSpec).AsComposeApplication()
	if err != nil {
		return nil, fmt.Errorf("getting compose application: %w", err)
	}

	appName := lo.FromPtr(composeApp.Name)
	envVars := lo.FromPtr(composeApp.EnvVars)
	volumes := composeApp.Volumes

	user := v1beta1.CurrentProcessUsername
	podman, err := podmanFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating podman client for user %s: %w", user, err)
	}

	readWriter, err := rwFactory(user)
	if err != nil {
		return nil, fmt.Errorf("creating read/writer for user %s: %w", user, err)
	}

	providerType, err := composeApp.Type()
	if err != nil {
		return nil, fmt.Errorf("getting compose provider type: %w", err)
	}

	if cfg != nil && cfg.providerTypes != nil {
		if _, exists := cfg.providerTypes[providerType]; !exists {
			return nil, nil
		}
	}

	var imageRef string
	var inlineContent []v1beta1.ApplicationContent

	switch providerType {
	case v1beta1.ImageApplicationProviderType:
		imageSpec, err := composeApp.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting compose image provider: %w", err)
		}
		imageRef = imageSpec.Image
		if appName == "" {
			appName = imageRef
		}

		if err := ensureAppTypeFromImage(ctx, podman, v1beta1.AppTypeCompose, imageRef); err != nil {
			return nil, fmt.Errorf("ensuring app type: %w", err)
		}

	case v1beta1.InlineApplicationProviderType:
		inlineSpec, err := composeApp.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting compose inline provider: %w", err)
		}
		inlineContent = inlineSpec.Inline
	}

	volumeManager, err := NewVolumeManager(log, appName, v1beta1.AppTypeCompose, user, volumes)
	if err != nil {
		return nil, err
	}

	appPath := filepath.Join(lifecycle.ComposeAppPath, appName)

	p := &composeProvider{
		log:           log,
		podman:        podman,
		readWriter:    readWriter,
		imageRef:      imageRef,
		inlineContent: inlineContent,
		spec: &ApplicationSpec{
			Name:       appName,
			ID:         lifecycle.GenerateAppID(appName, user),
			AppType:    v1beta1.AppTypeCompose,
			Path:       appPath,
			EnvVars:    envVars,
			ComposeApp: &composeApp,
			Volume:     volumeManager,
		},
	}

	if cfg != nil && cfg.appDataCache != nil {
		if cachedData, found := cfg.appDataCache[appName]; found {
			p.AppData = cachedData
		}
	}

	return p, nil
}

func (p *composeProvider) isImageBased() bool {
	return p.imageRef != ""
}

func (p *composeProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ComposeApp.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromAppType([]string{"docker-compose", "podman-compose"}); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureImageVolumes(lo.FromPtr(p.spec.ComposeApp.Volumes)); err != nil {
		return fmt.Errorf("%w: ensuring volumes: %w", errors.ErrNoRetry, err)
	}

	var tmpAppPath string
	var shouldCleanup bool
	if p.AppData != nil && p.AppData.TmpPath != "" {
		tmpAppPath = p.AppData.TmpPath
		shouldCleanup = false
	} else {
		var err error
		tmpAppPath, err = p.readWriter.MkdirTemp("app_temp")
		if err != nil {
			return fmt.Errorf("creating tmp dir: %w", err)
		}
		shouldCleanup = true

		if p.isImageBased() {
			if err := p.extractOCIContents(ctx, tmpAppPath); err != nil {
				return fmt.Errorf("extracting OCI contents: %w", err)
			}
		} else {
			if err := p.writeInlineContent(tmpAppPath); err != nil {
				return fmt.Errorf("writing inline content: %w", err)
			}
		}
	}

	defer func() {
		if shouldCleanup && tmpAppPath != "" {
			if err := p.readWriter.RemoveAll(tmpAppPath); err != nil {
				p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, err)
			}
		}
	}()

	if err := ensureCompose(p.readWriter, tmpAppPath); err != nil {
		return fmt.Errorf("%w: verifying compose: %w", errors.ErrNoRetry, err)
	}

	return nil
}

func (p *composeProvider) extractOCIContents(ctx context.Context, path string) error {
	return extractOCIContentsToPath(ctx, p.podman, p.log, p.readWriter, p.imageRef, path)
}

func (p *composeProvider) writeInlineContent(appPath string) error {
	return writeInlineContentToPath(p.readWriter, appPath, p.inlineContent)
}

func (p *composeProvider) Install(ctx context.Context) error {
	if p.AppData != nil {
		p.log.Debugf("Cleaning up cached app data before Install")
		if cleanupErr := p.AppData.Cleanup(); cleanupErr != nil {
			p.log.Warnf("Failed to cleanup cached app data: %v", cleanupErr)
		}
		p.AppData = nil
	}

	if p.isImageBased() {
		if err := p.extractOCIContents(ctx, p.spec.Path); err != nil {
			return fmt.Errorf("extracting OCI contents: %w", err)
		}
	} else {
		if err := p.writeInlineContent(p.spec.Path); err != nil {
			return fmt.Errorf("writing inline content: %w", err)
		}
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	if err := writeComposeOverride(p.log, p.spec.Path, p.spec.Volume, p.readWriter, client.ComposeOverrideFilename); err != nil {
		return fmt.Errorf("writing override file: %w", err)
	}

	return nil
}

func (p *composeProvider) Remove(ctx context.Context) error {
	if p.AppData != nil {
		p.log.Debugf("Cleaning up cached app data before Remove")
		if cleanupErr := p.AppData.Cleanup(); cleanupErr != nil {
			p.log.Warnf("Failed to cleanup cached app data: %v", cleanupErr)
		}
		p.AppData = nil
	}

	if err := p.readWriter.RemoveAll(p.spec.Path); err != nil {
		return fmt.Errorf("removing compose app path: %w", err)
	}
	return nil
}

func (p *composeProvider) Name() string {
	return p.spec.Name
}

func (p *composeProvider) ID() string {
	return p.spec.ID
}

func (p *composeProvider) Spec() *ApplicationSpec {
	return p.spec
}
