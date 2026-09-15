package generate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
	maxRegistryResponseBytes = 10 << 20
	maxReferrerPages         = 100
)

type deltaArtifact struct {
	Ref              string
	Manifest         ocispec.Descriptor
	PayloadSizeBytes int64
}

type deltaExistence struct {
	Exists   bool
	Artifact deltaArtifact
}

func checkExistingDelta(ctx context.Context, deltaRepository, sourceDigest, targetDigest string, spec *domain.OciRepoSpec) (*existingDelta, error) {
	if spec == nil {
		return nil, fmt.Errorf("OCI write target is required for existence check")
	}
	target, err := digest.Parse(targetDigest)
	if err != nil {
		return nil, fmt.Errorf("existence check: invalid target digest: %w", err)
	}
	source, err := digest.Parse(sourceDigest)
	if err != nil {
		return nil, fmt.Errorf("existence check: invalid source digest: %w", err)
	}
	repo, err := oci.BuildOciRepoRef(ctx, spec, deltaRepository)
	if err != nil {
		return nil, fmt.Errorf("existence check: configure repository: %w", err)
	}
	repo.MaxMetadataBytes = maxRegistryResponseBytes
	repo.ReferrerListMaxPages = maxReferrerPages
	existence, err := checkDeltaExists(ctx, repo, deltaRepository, source, target)
	if err != nil {
		return nil, fmt.Errorf("existence check: %w", err)
	}
	if !existence.Exists {
		return nil, nil
	}
	return &existingDelta{
		Ref:       existence.Artifact.Ref,
		SizeBytes: existence.Artifact.PayloadSizeBytes,
	}, nil
}

func checkDeltaExists(ctx context.Context, repo registry.Repository, deltaRepository string, sourceDigest, targetDigest digest.Digest) (deltaExistence, error) {
	var match *ocispec.Descriptor
	err := repo.Referrers(ctx, ocispec.Descriptor{Digest: targetDigest}, ociDeltaArtifactType, func(referrers []ocispec.Descriptor) error {
		if match != nil {
			return nil
		}
		if referrer, ok := matchingDeltaDescriptor(referrers, sourceDigest.String()); ok {
			match = &referrer
		}
		return nil
	})
	if err != nil {
		return deltaExistence{}, fmt.Errorf("list referrers: %w", err)
	}
	if match == nil {
		return deltaExistence{}, nil
	}
	if match.Size < 0 || match.Size > maxRegistryResponseBytes {
		return deltaExistence{}, fmt.Errorf("delta manifest size %d exceeds %d bytes", match.Size, maxRegistryResponseBytes)
	}
	manifestBytes, err := content.FetchAll(ctx, repo, *match)
	if err != nil {
		return deltaExistence{}, fmt.Errorf("fetch delta manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return deltaExistence{}, fmt.Errorf("invalid delta manifest: %w", err)
	}
	if err := validateDeltaManifest(manifest); err != nil {
		return deltaExistence{}, err
	}
	if manifest.Subject == nil || manifest.Subject.Digest != targetDigest {
		return deltaExistence{}, fmt.Errorf("delta subject does not match target %s", targetDigest)
	}
	if manifest.Annotations[ociDeltaSourceAnnotation] != sourceDigest.String() {
		return deltaExistence{}, fmt.Errorf("delta source does not match source %s", sourceDigest)
	}
	return deltaExistence{
		Exists: true,
		Artifact: deltaArtifact{
			Ref:              deltaRepository + "@" + match.Digest.String(),
			Manifest:         *match,
			PayloadSizeBytes: deltaPayloadSize(manifest),
		},
	}, nil
}

func deltaPayloadSize(manifest ocispec.Manifest) int64 {
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return size
}

func validateDeltaManifest(manifest ocispec.Manifest) error {
	if manifest.ArtifactType != ociDeltaArtifactType {
		return fmt.Errorf("unexpected artifact type %q", manifest.ArtifactType)
	}
	if manifest.Subject == nil || manifest.Subject.Digest == "" {
		return fmt.Errorf("delta manifest missing subject")
	}
	if manifest.Config.Digest == "" || manifest.Config.Size < 0 {
		return fmt.Errorf("delta manifest has invalid config descriptor")
	}
	for i, layer := range manifest.Layers {
		if layer.Digest == "" || layer.Size < 0 {
			return fmt.Errorf("delta manifest has invalid layer descriptor %d", i)
		}
	}
	if manifest.Annotations[ociDeltaSourceAnnotation] == "" {
		return fmt.Errorf("delta manifest missing %s annotation", ociDeltaSourceAnnotation)
	}
	return nil
}

func matchingDeltaDescriptor(referrers []ocispec.Descriptor, sourceDigest string) (ocispec.Descriptor, bool) {
	for _, referrer := range referrers {
		if referrer.ArtifactType != ociDeltaArtifactType {
			continue
		}
		if referrer.Annotations[ociDeltaSourceAnnotation] != sourceDigest || referrer.Digest == "" {
			continue
		}
		return referrer, true
	}
	return ocispec.Descriptor{}, false
}

func exactRepository(ctx context.Context, spec *domain.OciRepoSpec, repository string) (*remote.Repository, error) {
	if spec == nil {
		return nil, fmt.Errorf("OCI repository spec is required")
	}
	return oci.BuildOciRepoRef(ctx, spec, repository)
}
