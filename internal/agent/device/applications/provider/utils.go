package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/opencontainers/go-digest"
)

const (
	AppTypeLabel             = "appType"
	DefaultImageManifestDir  = "/"
	quadletImageVolumeDriver = "image"
)

// ensureAppTypeFromImage validates that the declared app type in the spec matches the appType label on the image (if one exists)
func ensureAppTypeFromImage(ctx context.Context, podman *client.Podman, declaredType v1beta1.AppType, image string) error {
	discoveredType, err := typeFromImage(ctx, podman, image)
	if err != nil {
		if errors.Is(err, errors.ErrAppLabel) {
			return nil
		}
		return err
	}
	if discoveredType != declaredType {
		return fmt.Errorf("%w: app type mismatch: declared %q discovered %q", errors.ErrAppLabel, declaredType, discoveredType)
	}
	return nil
}

// typeFromImage returns the app type from the OCI reference.
func typeFromImage(ctx context.Context, podman *client.Podman, image string) (v1beta1.AppType, error) {
	ociType, err := detectOCIType(ctx, podman, image)
	if err != nil {
		return "", err
	}

	var appTypeValue string
	var ok bool

	if ociType == dependency.OCITypePodmanArtifact {
		artifactInfo, err := podman.InspectArtifactAnnotations(ctx, image)
		if err != nil {
			return "", fmt.Errorf("inspecting artifact annotations: %w", err)
		}
		appTypeValue, ok = artifactInfo[AppTypeLabel]
	} else {
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
	if podman.ImageExists(ctx, imageRef) {
		return dependency.OCITypePodmanImage, nil
	}

	if podman.ArtifactExists(ctx, imageRef) {
		return dependency.OCITypePodmanArtifact, nil
	}

	return "", fmt.Errorf("OCI reference %s not found locally - cannot determine type", imageRef)
}

// availableOCIParent keeps cache identity in the same immutable representation
// used by extraction. Registry digest evidence is optional for usable contents.
func availableOCIParent(ctx context.Context, podman *client.Podman, logger *log.PrefixLogger, reference string) (string, string, bool, error) {
	if podman.ImageExists(ctx, reference) {
		imageID, err := podman.ImageID(ctx, reference)
		if err != nil {
			logger.Warnf("Cannot inspect local parent identity for %s: %v", reference, err)
			return reference, "", true, nil
		}
		value, err := podman.ImageDigest(ctx, imageID)
		if err != nil {
			logger.Warnf("Cannot inspect parent digest for %s; delta evidence will be withheld: %v", reference, err)
			value = ""
		}
		return reference, value, true, nil
	}
	if podman.ArtifactExists(ctx, reference) {
		value, err := podman.ArtifactDigest(ctx, reference)
		if err != nil {
			logger.Warnf("Cannot inspect parent artifact digest for %s; delta evidence will be withheld: %v", reference, err)
			value = ""
		}
		return reference, value, true, nil
	}
	return reference, "", false, nil
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

// extractOCIContentsToPath extracts OCI image or artifact contents to the specified path.
// It handles both podman images and artifacts, automatically detecting the type.
func extractOCIContentsToPath(ctx context.Context, podman *client.Podman, log *log.PrefixLogger, rw fileio.ReadWriter, imageRef, path string) error {
	_, _, err := extractOCIContentsToPathWithIdentity(ctx, podman, log, rw, imageRef, path)
	return err
}

// extractOCIContentsToPathWithIdentity extracts contents and returns the observed
// registry identity. Images are copied through their immutable local image ID;
// artifacts are extracted through their observed digest when it is available.
func extractOCIContentsToPathWithIdentity(ctx context.Context, podman *client.Podman, logger *log.PrefixLogger, rw fileio.ReadWriter, imageRef, path string) (string, dependency.OCIType, error) {
	clean := func() {
		if err := rw.RemoveAll(path); err != nil {
			logger.Warnf("Failed to cleanup directory %q: %v", path, err)
		}
	}

	ociType, err := detectOCIType(ctx, podman, imageRef)
	if err != nil {
		return "", "", fmt.Errorf("detecting OCI type: %w", err)
	}

	if ociType == dependency.OCITypePodmanArtifact {
		extractionReference, observedDigest, err := podman.ImmutableArtifactReference(ctx, imageRef)
		if err != nil {
			logger.Warnf("Cannot observe parent artifact digest for %s; delta evidence will be withheld: %v", imageRef, err)
			extractionReference = imageRef
		}
		if err := extractAndProcessArtifact(ctx, podman, logger, extractionReference, path, rw); err != nil {
			clean()
			return "", "", fmt.Errorf("extract artifact contents: %w", err)
		}
		return observedDigest, ociType, nil
	} else {
		imageID, err := podman.ImageID(ctx, imageRef)
		if err != nil {
			return "", "", fmt.Errorf("resolving immutable image identity: %w", err)
		}
		observedDigest, err := podman.ImageDigest(ctx, imageID)
		if err != nil {
			logger.Warnf("Cannot observe parent image digest for %s; delta evidence will be withheld: %v", imageRef, err)
			observedDigest = ""
		} else if _, err := digest.Parse(observedDigest); err != nil {
			logger.Warnf("Cannot verify parent image digest for %s; delta evidence will be withheld: %v", imageRef, err)
			observedDigest = ""
		}
		if err := podman.CopyContainerData(ctx, imageID, path); err != nil {
			clean()
			return "", "", fmt.Errorf("copy image contents: %w", err)
		}
		return observedDigest, ociType, nil
	}
}

// installOCIParentContents installs the exact parent contents used to collect
// nested targets when that extraction is still available. Otherwise it
// resolves and installs one immutable local image or artifact identity.
func installOCIParentContents(
	ctx context.Context,
	podman *client.Podman,
	logger *log.PrefixLogger,
	rw fileio.ReadWriter,
	imageRef, path string,
	appData *AppData,
) (string, dependency.OCIType, error) {
	if appData != nil && appData.TmpPath != "" && appData.ParentReference == imageRef && appData.ParentOCIType != "" {
		if _, err := digest.Parse(appData.ParentDigest); appData.ParentDigest == "" || err == nil {
			// The retained extraction is a private temporary directory. Create the
			// installed directory with its normal permissions and application owner.
			if err := rw.MkdirAll(path, fileio.DefaultDirectoryPermissions); err != nil {
				return "", "", fmt.Errorf("creating application directory %w: %w", errors.WithElement(path), err)
			}
			if err := rw.CopyDir(appData.TmpPath, path, fileio.WithDirectoryOwnership()); err != nil {
				if rmErr := rw.RemoveAll(path); rmErr != nil {
					logger.Warnf("Failed to cleanup incomplete parent copy at %q: %v", path, rmErr)
				}
				logger.Warnf("Failed to reuse collected parent contents at %q: %v; extracting from %q", appData.TmpPath, err, imageRef)
			} else {
				return appData.ParentDigest, appData.ParentOCIType, nil
			}
		}
	}
	return extractOCIContentsToPathWithIdentity(ctx, podman, logger, rw, imageRef, path)
}

// bindQuadletImageVolumes pins executable image volumes to the local immutable
// Podman image ID and labels the same Quadlet resource with the digest observed
// from that ID. If runtime identity cannot be verified, the unit remains usable
// through its original reference but status collection will fail closed.
func bindQuadletImageVolumes(
	ctx context.Context,
	podman *client.Podman,
	rw fileio.ReadWriter,
	appPath, applicationID, lifetime string,
	owner v1beta1.Username,
	logger *log.PrefixLogger,
) error {
	entries, err := rw.ReadDir(appPath)
	if err != nil {
		return fmt.Errorf("reading Quadlet application directory %w: %w", errors.WithElement(appPath), err)
	}
	if podman == nil {
		logger.Warn("Cannot bind Quadlet image volumes without the owning Podman client")
		return nil
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != quadlet.VolumeExtension {
			continue
		}
		path := filepath.Join(appPath, entry.Name())
		contents, err := rw.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading Quadlet volume %w: %w", errors.WithElement(path), err)
		}
		unit, err := quadlet.NewUnit(contents)
		if err != nil {
			return fmt.Errorf("parsing Quadlet volume %w: %w", errors.WithElement(path), err)
		}
		if !unit.HasSection(quadlet.VolumeGroup) {
			continue
		}
		driver, err := unit.Lookup(quadlet.VolumeGroup, quadlet.DriverKey)
		if err != nil || driver != quadletImageVolumeDriver {
			continue
		}
		reference, err := unit.Lookup(quadlet.VolumeGroup, quadlet.ImageKey)
		if err != nil || strings.TrimSpace(reference) == "" {
			logger.Warnf("Cannot bind Quadlet image volume %s without an image reference", entry.Name())
			continue
		}

		imageID, err := podman.ImageID(ctx, reference)
		if err != nil || !client.IsImmutablePodmanImageID(imageID) {
			logger.Warnf("Cannot bind Quadlet image volume %s to an immutable image ID: %v", entry.Name(), err)
			continue
		}
		if err := unit.Transform(quadlet.VolumeGroup, quadlet.ImageKey, func(string) (string, error) {
			return imageID, nil
		}); err != nil {
			return fmt.Errorf("pinning image for Quadlet volume %w: %w", errors.WithElement(path), err)
		}

		imageDigest, err := podman.ImageDigest(ctx, imageID)
		if err != nil || !validProviderImageDigest(imageDigest) {
			logger.Warnf("Cannot bind Quadlet image volume %s to a verified registry digest: %v", entry.Name(), err)
		} else if applicationID != "" && lifetime != "" {
			labels := map[string]string{
				client.PodmanDeltaVolumeApplicationLabel: applicationID,
				client.PodmanDeltaVolumeLifetimeLabel:    lifetime,
				client.PodmanDeltaVolumeReferenceLabel:   reference,
				client.PodmanDeltaVolumeDigestLabel:      imageDigest,
				client.PodmanDeltaVolumeOwnerLabel:       string(owner.WithDefault(v1beta1.RootUsername)),
				client.PodmanDeltaVolumeOCITypeLabel:     string(dependency.OCITypePodmanImage),
			}
			if err := setQuadletVolumeLabels(unit, labels); err != nil {
				return fmt.Errorf("stamping Quadlet image volume %w: %w", errors.WithElement(path), err)
			}
		}
		updated, err := unit.Write()
		if err != nil {
			return fmt.Errorf("serializing Quadlet volume %w: %w", errors.WithElement(path), err)
		}
		if err := rw.WriteFile(path, updated, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("writing Quadlet volume %w: %w", errors.WithElement(path), err)
		}
	}
	return nil
}

func setQuadletVolumeLabels(unit *quadlet.Unit, labels map[string]string) error {
	set := make(map[string]bool, len(labels))
	if err := unit.TransformAll(quadlet.VolumeGroup, func(key, value string) (string, error) {
		if key != quadlet.LabelKey {
			return value, nil
		}
		labelKey, _, ok := strings.Cut(value, "=")
		if !ok {
			return value, nil
		}
		labelValue, exists := labels[labelKey]
		if !exists {
			return value, nil
		}
		set[labelKey] = true
		return labelKey + "=" + labelValue, nil
	}); err != nil {
		return err
	}
	for key, value := range labels {
		if !set[key] {
			unit.Add(quadlet.VolumeGroup, quadlet.LabelKey, key+"="+value)
		}
	}
	return nil
}

func validProviderImageDigest(value string) bool {
	_, err := digest.Parse(value)
	return err == nil
}

// writeInlineContentToPath writes inline application content to the specified path.
func writeInlineContentToPath(rw fileio.ReadWriter, appPath string, contents []v1beta1.ApplicationContent) error {
	if err := rw.MkdirAll(appPath, fileio.DefaultDirectoryPermissions); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}
	for _, content := range contents {
		contentBytes, err := content.ContentsDecoded()
		if err != nil {
			return fmt.Errorf("decoding application content: %w", err)
		}
		contentPath := content.Path
		if len(contentPath) == 0 {
			return fmt.Errorf("application content path is empty")
		}
		if err := rw.WriteFile(filepath.Join(appPath, contentPath), contentBytes, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("writing application content: %w", err)
		}
	}
	return nil
}
