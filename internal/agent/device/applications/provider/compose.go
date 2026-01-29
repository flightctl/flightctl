package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

var composeBinaryDeps = []dependencyBins{
	{variants: []string{"podman"}},
	{variants: []string{"docker-compose", "podman-compose"}},
	{variants: []string{"skopeo"}},
}

var _ Provider = (*composeProvider)(nil)
var _ appProvider = (*composeProvider)(nil)

type composeProvider struct {
	log            *log.PrefixLogger
	podman         *client.Podman
	readWriter     fileio.ReadWriter
	commandChecker commandChecker
	spec           *ApplicationSpec

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
		log:            log,
		podman:         podman,
		readWriter:     readWriter,
		commandChecker: client.IsCommandAvailable,
		imageRef:       imageRef,
		inlineContent:  inlineContent,
		spec: &ApplicationSpec{
			Name:       appName,
			User:       user,
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

func (p *composeProvider) EnsureDependencies(ctx context.Context) error {
	if err := ensureDependenciesFromAppType(composeBinaryDeps, p.commandChecker); err != nil {
		return err
	}
	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ComposeApp.Volumes); err != nil {
		return fmt.Errorf("%w: volume dependencies: %w", errors.ErrAppDependency, err)
	}
	return nil
}

func (p *composeProvider) Verify(ctx context.Context) error {
	if err := p.EnsureDependencies(ctx); err != nil {
		return err
	}

	if p.isImageBased() {
		if err := ensureAppTypeFromImage(ctx, p.podman, v1beta1.AppTypeCompose, p.imageRef); err != nil {
			return fmt.Errorf("ensuring app type: %w", err)
		}
	}

	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
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

func (p *composeProvider) collectOCITargets(ctx context.Context, configProvider dependency.PullConfigResolver) (dependency.OCIPullTargetsByUser, error) {
	var targets dependency.OCIPullTargetsByUser
	if p.imageRef != "" {
		targets = targets.Add(p.spec.User, dependency.OCIPullTarget{
			Type:         dependency.OCITypeAuto,
			Reference:    p.imageRef,
			PullPolicy:   v1beta1.PullIfNotPresent,
			ClientOptsFn: containerPullOptions(configProvider),
		})
	} else {
		composeSpec, err := client.ParseComposeFromSpec(p.inlineContent)
		if err != nil {
			return nil, fmt.Errorf("parsing compose spec: %w", err)
		}
		for _, svc := range composeSpec.Services {
			if svc.Image != "" {
				targets = targets.Add(p.spec.User, dependency.OCIPullTarget{
					Type:         dependency.OCITypePodmanImage,
					Reference:    svc.Image,
					PullPolicy:   v1beta1.PullIfNotPresent,
					ClientOptsFn: containerPullOptions(configProvider),
				})
			}
		}
	}
	volTargets, err := extractVolumeTargets(p.spec.ComposeApp.Volumes, configProvider)
	if err != nil {
		return nil, fmt.Errorf("extracting compose volume targets: %w", err)
	}
	return targets.Add(p.spec.User, volTargets...), nil
}

func (p *composeProvider) extractNestedTargets(ctx context.Context, configProvider dependency.PullConfigResolver) (*AppData, error) {
	if !p.isImageBased() {
		return &AppData{}, nil
	}

	appData, err := extractAppDataFromOCITarget(ctx, p.podman, p.readWriter, p.spec.Name, p.imageRef, v1beta1.AppTypeCompose, configProvider)
	if err != nil {
		return nil, err
	}
	return appData, nil
}

func (p *composeProvider) parentIsAvailable(ctx context.Context) (string, string, bool, error) {
	if !p.isImageBased() {
		return "", "", true, nil
	}

	if p.podman.ImageExists(ctx, p.imageRef) {
		digest, err := p.podman.ImageDigest(ctx, p.imageRef)
		if err != nil {
			return "", "", false, fmt.Errorf("getting image digest: %w", err)
		}
		return p.imageRef, digest, true, nil
	}

	if p.podman.ArtifactExists(ctx, p.imageRef) {
		digest, err := p.podman.ArtifactDigest(ctx, p.imageRef)
		if err != nil {
			return "", "", false, fmt.Errorf("getting artifact digest: %w", err)
		}
		return p.imageRef, digest, true, nil
	}

	return p.imageRef, "", false, nil
}
