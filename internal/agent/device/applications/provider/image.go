package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/v1beta1"
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
	podman     *client.Podman
	readWriter fileio.ReadWriter
	log        *log.PrefixLogger
	handler    appTypeHandler
	spec       *ApplicationSpec

	// AppData stores the extracted app data from OCITargets to reuse in Verify
	AppData *AppData
}

func newImageHandler(appType v1beta1.AppType, name string, rw fileio.ReadWriter, l *log.PrefixLogger, podman *client.Podman, vm VolumeManager, provider *v1beta1.ImageApplicationProviderSpec) (appTypeHandler, error) {
	switch appType {
	case v1beta1.AppTypeQuadlet:
		qb := &quadletHandler{
			name:        name,
			rw:          rw,
			specVolumes: lo.FromPtr(provider.Volumes),
		}
		qb.volumeProvider = func() ([]*Volume, error) {
			return extractQuadletVolumesFromDir(qb.ID(), rw, qb.AppPath())
		}
		return qb, nil
	case v1beta1.AppTypeCompose:
		return &composeHandler{
			name:        name,
			rw:          rw,
			log:         l,
			vm:          vm,
			specVolumes: lo.FromPtr(provider.Volumes),
		}, nil
	case v1beta1.AppTypeContainer:
		return &containerHandler{
			name:   name,
			rw:     rw,
			podman: podman,
			spec:   provider,
		}, nil
	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}
}

func newImage(log *log.PrefixLogger, podman *client.Podman, spec *v1beta1.ApplicationProviderSpec, readWriter fileio.ReadWriter, appType v1beta1.AppType) (*imageProvider, error) {
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

	handler, err := newImageHandler(appType, appName, readWriter, log, podman, volumeManager, &provider)
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

	if err := ensureDependenciesFromAppType(p.handler); err != nil {
		return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
	}

	if err := ensureDependenciesFromVolumes(ctx, p.podman, p.spec.ImageProvider.Volumes); err != nil {
		return fmt.Errorf("%w: ensuring volume dependencies: %w", errors.ErrNoRetry, err)
	}

	ociType, err := detectOCIType(ctx, p.podman, p.spec.ImageProvider.Image)
	if err != nil {
		return fmt.Errorf("detecting OCI type: %w", err)
	}

	var tmpAppPath string
	var shouldCleanup bool

	if p.AppData != nil {
		tmpAppPath = p.AppData.TmpPath
		shouldCleanup = false
	} else {
		// no cache, extract the OCI contents
		var err error
		tmpAppPath, err = p.readWriter.MkdirTemp("app_temp")
		if err != nil {
			return fmt.Errorf("creating tmp dir: %w", err)
		}
		shouldCleanup = true

		if ociType == dependency.OCITypeArtifact {
			if err := extractAndProcessArtifact(ctx, p.podman, p.log, p.spec.ImageProvider.Image, tmpAppPath, p.readWriter); err != nil {
				if rmErr := p.readWriter.RemoveAll(tmpAppPath); rmErr != nil {
					p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, rmErr)
				}
				return fmt.Errorf("extract artifact contents: %w", err)
			}
		} else {
			if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, tmpAppPath); err != nil {
				if rmErr := p.readWriter.RemoveAll(tmpAppPath); rmErr != nil {
					p.log.Warnf("Failed to cleanup temporary directory %q: %v", tmpAppPath, rmErr)
				}
				return fmt.Errorf("copy image contents: %w", err)
			}
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

	ociType, err := detectOCIType(ctx, p.podman, p.spec.ImageProvider.Image)
	if err != nil {
		return fmt.Errorf("detecting OCI type: %w", err)
	}

	if ociType == dependency.OCITypeArtifact {
		if err := extractAndProcessArtifact(ctx, p.podman, p.log, p.spec.ImageProvider.Image, p.spec.Path, p.readWriter); err != nil {
			return fmt.Errorf("extract artifact contents: %w", err)
		}
	} else {
		if err := p.podman.CopyContainerData(ctx, p.spec.ImageProvider.Image, p.spec.Path); err != nil {
			return fmt.Errorf("copy image contents: %w", err)
		}
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

// typeFromImage returns the app type from the OCI reference.
func typeFromImage(ctx context.Context, podman *client.Podman, image string) (v1beta1.AppType, error) {
	ociType, err := detectOCIType(ctx, podman, image)
	if err != nil {
		return "", err
	}

	var appTypeValue string
	var ok bool

	if ociType == dependency.OCITypeArtifact {
		// For artifacts, check annotations
		artifactInfo, err := podman.InspectArtifactAnnotations(ctx, image)
		if err != nil {
			return "", fmt.Errorf("inspecting artifact annotations: %w", err)
		}
		appTypeValue, ok = artifactInfo[AppTypeLabel]
	} else {
		// For images, check labels
		labels, err := podman.InspectLabels(ctx, image)
		if err != nil {
			return "", err
		}
		appTypeValue, ok = labels[AppTypeLabel]
	}

	if !ok {
		return "", fmt.Errorf("%w: %s, %s", errors.ErrAppLabel, AppTypeLabel, image)
	}

	appType := v1beta1.AppType(appTypeValue)
	if appType == "" {
		return "", fmt.Errorf("%w: %s", errors.ErrParseAppType, appTypeValue)
	}
	return appType, nil
}

// detectOCIType determines the OCI type (image or artifact) of a reference
func detectOCIType(ctx context.Context, podman *client.Podman, imageRef string) (dependency.OCIType, error) {
	// Check if it exists as an image first (most common case)
	if podman.ImageExists(ctx, imageRef) {
		return dependency.OCITypeImage, nil
	}

	// Check if it exists as an artifact
	if podman.ArtifactExists(ctx, imageRef) {
		return dependency.OCITypeArtifact, nil
	}

	// Reference doesn't exist locally - this shouldn't happen after prefetch
	return "", fmt.Errorf("OCI reference %s not found locally - cannot determine type", imageRef)
}

// extractAndProcessArtifact extracts an artifact and handles tar/tar.gz files.
func extractAndProcessArtifact(ctx context.Context, podman *client.Podman, log *log.PrefixLogger, artifact, destination string, writer fileio.ReadWriter) error {
	tmpDir, err := writer.MkdirTemp("artifact_extract")
	if err != nil {
		return fmt.Errorf("creating temp directory: %w", err)
	}
	defer func() {
		if rmErr := writer.RemoveAll(tmpDir); rmErr != nil {
			log.Warnf("Failed to cleanup temp directory %q: %v", tmpDir, rmErr)
		}
	}()

	// Extract artifact to temp directory
	if _, err := podman.ExtractArtifact(ctx, artifact, tmpDir); err != nil {
		return fmt.Errorf("extracting artifact: %w", err)
	}

	if err := writer.MkdirAll(destination, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	entries, err := writer.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("reading extracted content: %w", err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(tmpDir, entry.Name())

		if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".tar") || strings.HasSuffix(entry.Name(), ".tar.gz") || strings.HasSuffix(entry.Name(), ".tgz")) {
			if err := fileio.UnpackTar(writer, srcPath, destination); err != nil {
				return fmt.Errorf("unpacking tar file %s: %w", entry.Name(), err)
			}
		} else {
			destPath := filepath.Join(destination, entry.Name())
			if entry.IsDir() {
				if err := writer.CopyDir(srcPath, destPath); err != nil {
					return fmt.Errorf("copying directory %s: %w", entry.Name(), err)
				}
			} else {
				if err := writer.CopyFile(srcPath, destPath); err != nil {
					return fmt.Errorf("copying file %s: %w", entry.Name(), err)
				}
			}
		}
	}

	return nil
}
