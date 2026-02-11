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
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

const (
	AppTypeLabel            = "appType"
	DefaultImageManifestDir = "/"
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
	clean := func() {
		if err := rw.RemoveAll(path); err != nil {
			log.Warnf("Failed to cleanup directory %q: %v", path, err)
		}
	}

	ociType, err := detectOCIType(ctx, podman, imageRef)
	if err != nil {
		return fmt.Errorf("detecting OCI type: %w", err)
	}

	if ociType == dependency.OCITypePodmanArtifact {
		if err := extractAndProcessArtifact(ctx, podman, log, imageRef, path, rw); err != nil {
			clean()
			return fmt.Errorf("extract artifact contents: %w", err)
		}
	} else {
		if err := podman.CopyContainerData(ctx, imageRef, path); err != nil {
			clean()
			return fmt.Errorf("copy image contents: %w", err)
		}
	}
	return nil
}

// writeInlineContentToPath writes inline application content to the specified path.
func writeInlineContentToPath(rw fileio.ReadWriter, appPath string, contents []v1beta1.ApplicationContent) error {
	if err := rw.MkdirAll(appPath, fileio.DefaultDirectoryPermissions); err != nil {
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
		if err := rw.WriteFile(filepath.Join(appPath, contentPath), contentBytes, fileio.DefaultFilePermissions); err != nil {
			return fmt.Errorf("writing application content: %w", err)
		}
	}
	return nil
}
