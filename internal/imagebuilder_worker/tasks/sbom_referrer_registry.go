package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2/registry/remote"
)

// resolveReferencedDigest resolves the destination image manifest for attaching an SBOM referrer.
// If the manifest is an image index (multi-arch), it picks linux/amd64 (same behavior as image export).
// onRegistryOutput is optional (e.g. ImageBuild statusUpdater.ReportOutput).
func resolveReferencedDigest(
	ctx context.Context,
	repoRef *remote.Repository,
	imageTag string,
	destRef string,
	onRegistryOutput func([]byte),
	log logrus.FieldLogger,
) (ocispec.Descriptor, error) {
	if onRegistryOutput != nil {
		onRegistryOutput([]byte(fmt.Sprintf("Resolving destination image manifest for tag: %s\n", imageTag)))
	}
	destManifestDesc, err := repoRef.Resolve(ctx, imageTag)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to resolve destination image manifest: %w", err)
	}

	targetPlatform := "linux/amd64"
	if destManifestDesc.MediaType == ocispec.MediaTypeImageIndex {
		log.WithField("mediaType", destManifestDesc.MediaType).Info("Resolved manifest list, finding platform-specific manifest")
		if onRegistryOutput != nil {
			onRegistryOutput([]byte(fmt.Sprintf("Resolved manifest list, finding platform-specific manifest for %s\n", targetPlatform)))
		}

		indexReader, err := repoRef.Fetch(ctx, destManifestDesc)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to fetch manifest list: %w", err)
		}
		defer indexReader.Close()

		indexBytes, err := io.ReadAll(indexReader)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to read manifest list: %w", err)
		}

		var index ocispec.Index
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		var platformManifest *ocispec.Descriptor
		for _, manifest := range index.Manifests {
			if manifest.Platform != nil {
				platformStr := fmt.Sprintf("%s/%s", manifest.Platform.OS, manifest.Platform.Architecture)
				if platformStr == targetPlatform {
					platformManifest = &manifest
					break
				}
			}
		}

		if platformManifest == nil {
			return ocispec.Descriptor{}, fmt.Errorf("platform %q not found in manifest list", targetPlatform)
		}

		destManifestDesc = *platformManifest
		log.WithFields(logrus.Fields{
			"platform":       targetPlatform,
			"manifestDigest": destManifestDesc.Digest.String(),
		}).Info("Found platform-specific manifest in manifest list")
		if onRegistryOutput != nil {
			onRegistryOutput([]byte(fmt.Sprintf("Found platform-specific manifest: %s\n", destManifestDesc.Digest.String())))
		}
	}

	log.WithFields(logrus.Fields{
		"subject":       fmt.Sprintf("%s:%s", destRef, imageTag),
		"subjectDigest": destManifestDesc.Digest.String(),
		"mediaType":     destManifestDesc.MediaType,
	}).Info("Resolved destination image manifest for referrer")
	if onRegistryOutput != nil {
		onRegistryOutput([]byte(fmt.Sprintf("Resolved destination image manifest for tag %s (Digest: %s, MediaType: %s)\n", imageTag, destManifestDesc.Digest.String(), destManifestDesc.MediaType)))
	}

	return destManifestDesc, nil
}
