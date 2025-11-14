package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
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
	podman          *client.Podman
	readWriter      fileio.ReadWriter
	prefetchManager dependency.PrefetchManager
	pullSecret      *client.PullSecret
	log             *log.PrefixLogger
	spec            *ApplicationSpec
}

func newImage(log *log.PrefixLogger, podman *client.Podman, spec *v1alpha1.ApplicationProviderSpec, readWriter fileio.ReadWriter, prefetchManager dependency.PrefetchManager, pullSecret *client.PullSecret) (*imageProvider, error) {
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

	// AppType will be detected from image label/artifact annotation during Verify if not specified
	appType := lo.FromPtr(spec.AppType)

	// Use a default path temporarily; it will be set correctly in Verify once appType is determined
	path := ""
	if appType != "" {
		path, err = pathFromAppType(appType, appName, embedded)
		if err != nil {
			return nil, fmt.Errorf("getting app path: %w", err)
		}
	}

	volumeManager, err := NewVolumeManager(log, appName, provider.Volumes)
	if err != nil {
		return nil, err
	}

	return &imageProvider{
		log:             log,
		podman:          podman,
		readWriter:      readWriter,
		prefetchManager: prefetchManager,
		pullSecret:      pullSecret,
		spec: &ApplicationSpec{
			Name:          appName,
			AppType:       appType,
			Path:          path,
			EnvVars:       lo.FromPtr(spec.EnvVars),
			Embedded:      embedded,
			ImageProvider: &provider,
			Volume:        volumeManager,
		},
	}, nil
}

func (p *imageProvider) OCITargets(pullSecret *client.PullSecret) ([]dependency.OCIPullTarget, error) {
	policy := v1alpha1.PullIfNotPresent
	var targets []dependency.OCIPullTarget

	// For OCITargets, we can't know if it's an artifact yet (no context available)
	// We'll assume it's an image for now, and the actual determination happens in Verify
	// The prefetch manager will handle both images and artifacts appropriately
	targets = append(targets, dependency.OCIPullTarget{
		Type:       dependency.OCITypeImage,
		Reference:  p.spec.ImageProvider.Image,
		PullPolicy: policy,
		PullSecret: pullSecret,
	})

	// volume artifacts
	volTargets, err := extractVolumeTargets(p.spec.ImageProvider.Volumes, pullSecret)
	if err != nil {
		return nil, fmt.Errorf("parsing volume targets: %w", err)
	}
	targets = append(targets, volTargets...)

	return targets, nil
}

func (p *imageProvider) Verify(ctx context.Context) error {
	if err := validateEnvVars(p.spec.EnvVars); err != nil {
		return fmt.Errorf("%w: validating env vars: %w", errors.ErrInvalidSpec, err)
	}

	reference := p.spec.ImageProvider.Image

	// Get app type from spec or auto-detect from labels/annotations
	specDefinedType := lo.FromPtr(&p.spec.AppType)
	detectedType, isArtifact, err := inspectReference(ctx, p.podman, reference)

	if specDefinedType == "" {
		// Auto-detect type from artifact annotation or image label
		if err != nil {
			return fmt.Errorf("getting app type: %w", err)
		}
		p.spec.AppType = detectedType
	} else {
		// Spec has app type defined - validate it matches artifact/image metadata
		if err == nil && detectedType != "" && detectedType != specDefinedType {
			sourceType := "image label"
			if isArtifact {
				sourceType = "artifact annotation"
			}
			p.log.Warnf("App type mismatch: spec defines %q but %s has %q. Using spec definition.",
				specDefinedType, sourceType, detectedType)
		}
	}

	// Validate supported app types
	if p.spec.AppType != v1alpha1.AppTypeCompose && p.spec.AppType != v1alpha1.AppTypeQuadlet {
		return fmt.Errorf("%w: %s (image provider supports compose and quadlet)", errors.ErrUnsupportedAppType, p.spec.AppType)
	}

	if err := ensureDependenciesFromAppType(p.spec.AppType); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ImageProvider.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	// create a temporary directory to extract/copy the contents
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

	// Extract or copy contents based on whether it's an artifact or image
	if isArtifact {
		// Verify Podman version supports artifact extraction (requires >= 5.5.0)
		version, err := p.podman.Version(ctx)
		if err != nil {
			return fmt.Errorf("%w: checking podman version: %w", errors.ErrNoRetry, err)
		}
		if !version.GreaterOrEqual(5, 5) {
			return fmt.Errorf("%w: OCI artifact extraction requires podman >= 5.5, found %d.%d", errors.ErrNoRetry, version.Major, version.Minor)
		}

		p.log.Infof("Extracting artifact contents: %s", reference)
		if _, err := p.podman.ExtractArtifact(ctx, reference, tmpAppPath); err != nil {
			return fmt.Errorf("extract artifact contents: %w", err)
		}
	} else {
		p.log.Debugf("Copying image contents: %s", reference)
		if err := p.podman.CopyContainerData(ctx, reference, tmpAppPath); err != nil {
			return fmt.Errorf("copy image contents: %w", err)
		}
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		p.spec.ID = client.NewComposeID(p.spec.Name)
		path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
		if err != nil {
			return fmt.Errorf("getting app path: %w", err)
		}
		p.spec.Path = path

		// ensure the compose application content in tmp dir is valid
		if err := ensureCompose(p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring compose: %w", err)
		}
	case v1alpha1.AppTypeQuadlet:
		p.spec.ID = client.NewComposeID(p.spec.Name)
		path, err := pathFromAppType(p.spec.AppType, p.spec.Name, p.spec.Embedded)
		if err != nil {
			return fmt.Errorf("getting app path: %w", err)
		}
		p.spec.Path = path

		// ensure the quadlet application content in tmp dir is valid
		if err := ensureQuadlet(p.readWriter, tmpAppPath); err != nil {
			return fmt.Errorf("ensuring quadlet: %w", err)
		}

		// Note: Images referenced in quadlet files are not pre-fetched.
		// Podman/systemd will automatically pull them when starting the services.
	default:
		return fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, p.spec.AppType)
	}

	return nil
}

func (p *imageProvider) Install(ctx context.Context) error {
	if p.spec.ImageProvider == nil {
		return fmt.Errorf("image application spec is nil")
	}

	reference := p.spec.ImageProvider.Image

	// Determine if this is an artifact or image at runtime
	_, isArtifact, err := inspectReference(ctx, p.podman, reference)
	if err != nil {
		return fmt.Errorf("inspecting reference: %w", err)
	}

	// Extract or copy contents based on whether it's an artifact or image
	if isArtifact {
		// Verify Podman version supports artifact extraction (requires >= 5.5.0)
		version, err := p.podman.Version(ctx)
		if err != nil {
			return fmt.Errorf("%w: checking podman version: %w", errors.ErrNoRetry, err)
		}
		if !version.GreaterOrEqual(5, 5) {
			return fmt.Errorf("%w: OCI artifact extraction requires podman >= 5.5, found %d.%d", errors.ErrNoRetry, version.Major, version.Minor)
		}

		p.log.Infof("Extracting artifact contents: %s", reference)
		if _, err := p.podman.ExtractArtifact(ctx, reference, p.spec.Path); err != nil {
			return fmt.Errorf("extract artifact contents: %w", err)
		}
	} else {
		p.log.Debugf("Copying image contents: %s", reference)
		if err := p.podman.CopyContainerData(ctx, reference, p.spec.Path); err != nil {
			return fmt.Errorf("copy image contents: %w", err)
		}
	}

	if err := writeENVFile(p.spec.Path, p.readWriter, p.spec.EnvVars); err != nil {
		return fmt.Errorf("writing env file: %w", err)
	}

	switch p.spec.AppType {
	case v1alpha1.AppTypeCompose:
		if err := writeComposeOverride(p.log, p.spec.Path, p.spec.Volume, p.readWriter, client.ComposeOverrideFilename); err != nil {
			return fmt.Errorf("writing override file %w", err)
		}
	case v1alpha1.AppTypeQuadlet:
		if err := installQuadlet(p.readWriter, p.spec.Path, p.spec.ID); err != nil {
			return fmt.Errorf("installing quadlet: %w", err)
		}
	}

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

// inspectReference returns app type and whether the reference is an artifact (true) or image (false).
// It inspects the reference once and checks for annotations (artifact) or labels (image).
func inspectReference(ctx context.Context, podman *client.Podman, reference string) (appType v1alpha1.AppType, isArtifact bool, err error) {
	inspectJSON, err := podman.Inspect(ctx, reference)
	if err != nil {
		return "", false, err
	}

	var inspectData []struct {
		Annotations map[string]string `json:"Annotations"`
		Config      struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := json.Unmarshal([]byte(inspectJSON), &inspectData); err != nil {
		return "", false, fmt.Errorf("parse inspect response: %w", err)
	}

	if len(inspectData) == 0 {
		return "", false, fmt.Errorf("no inspect data found")
	}

	// Check for annotations first (OCI artifacts), then fall back to labels (images)
	var metadata map[string]string
	if len(inspectData[0].Annotations) > 0 {
		metadata = inspectData[0].Annotations
		isArtifact = true
	} else if len(inspectData[0].Config.Labels) > 0 {
		metadata = inspectData[0].Config.Labels
		isArtifact = false
	} else {
		metadata = make(map[string]string)
		isArtifact = false
	}

	appType, err = extractAppType(metadata, AppTypeLabel, reference)
	return appType, isArtifact, err
}

// extractAppType extracts and validates appType from a metadata map (labels or annotations)
func extractAppType(metadata map[string]string, key, reference string) (v1alpha1.AppType, error) {
	appTypeValue, ok := metadata[key]
	if !ok {
		return "", fmt.Errorf("%w: %s, %s", errors.ErrAppLabel, key, reference)
	}
	appType := v1alpha1.AppType(appTypeValue)
	if appType == "" {
		return "", fmt.Errorf("%w: %s", errors.ErrParseAppType, appTypeValue)
	}
	return appType, nil
}
