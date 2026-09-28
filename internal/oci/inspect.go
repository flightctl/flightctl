package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const ImageDigestCacheTTL = 15 * time.Minute

type DigestCache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	SetNX(ctx context.Context, key string, value []byte) (bool, error)
	SetExpire(ctx context.Context, key string, expiration time.Duration) error
}

func SpecForRegistry(host string, spec *domain.OciRepoSpec) *domain.OciRepoSpec {
	empty := &domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci}
	if spec == nil || spec.Registry != host {
		return empty
	}
	return spec
}

func RemoteRepository(ctx context.Context, spec *domain.OciRepoSpec, imageRef string) (*remote.Repository, string, error) {
	imageRef, err := RewriteImageRef(imageRef)
	if err != nil {
		return nil, "", err
	}
	parsed, err := registry.ParseReference(strings.TrimPrefix(imageRef, "docker://"))
	if err != nil {
		return nil, "", fmt.Errorf("parse image reference: %w", err)
	}
	if parsed.Reference == "" {
		return nil, "", fmt.Errorf("image reference %q has no tag or digest", imageRef)
	}
	repo, err := BuildOciRepoRef(ctx, SpecForRegistry(parsed.Registry, spec), parsed.Registry+"/"+parsed.Repository)
	if err != nil {
		return nil, "", fmt.Errorf("create repository reference: %w", err)
	}
	return repo, parsed.Reference, nil
}

func DigestFromImageRef(imageRef string) (string, error) {
	rewritten, err := RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	named, err := reference.ParseNormalizedNamed(strings.TrimPrefix(rewritten, "docker://"))
	if err != nil {
		return "", err
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return "", nil
	}
	return digested.Digest().String(), nil
}

func InspectImageDigest(ctx context.Context, image string, spec *domain.OciRepoSpec) (string, error) {
	dgst, err := DigestFromImageRef(image)
	if err != nil {
		return "", err
	}
	if dgst != "" {
		return dgst, nil
	}
	repo, ref, err := RemoteRepository(ctx, spec, image)
	if err != nil {
		return "", err
	}
	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("resolve image digest for %s: %w", image, err)
	}
	return desc.Digest.String(), nil
}

// InspectImagePayloadSize returns the config and layer payload size for an OCI
// image. When targetDigest is set, it is used instead of resolving imageRef so
// a mutable tag cannot change between digest and size inspection.
func InspectImagePayloadSize(ctx context.Context, imageRef, targetDigest string, spec *domain.OciRepoSpec, platform *ocispec.Platform) (*int64, error) {
	repo, ref, err := RemoteRepository(ctx, spec, imageRef)
	if err != nil {
		return nil, err
	}
	if targetDigest != "" {
		ref = targetDigest
	}
	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("resolve image manifest for %s: %w", imageRef, err)
	}
	size, err := imagePayloadSize(ctx, repo, desc, platform, 0)
	if err != nil {
		return nil, fmt.Errorf("inspect image payload size for %s: %w", imageRef, err)
	}
	return &size, nil
}

const maxImageIndexDepth = 8

const dockerManifestListMediaType = "application/vnd.docker.distribution.manifest.list.v2+json"
const dockerManifestV2MediaType = "application/vnd.docker.distribution.manifest.v2+json"

func imagePayloadSize(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, platform *ocispec.Platform, depth int) (int64, error) {
	if depth > maxImageIndexDepth {
		return 0, fmt.Errorf("image index nesting exceeds %d", maxImageIndexDepth)
	}
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestListMediaType:
		manifest, err := selectPlatformManifest(ctx, repo, desc, platform)
		if err != nil {
			return 0, err
		}
		return imagePayloadSize(ctx, repo, manifest, platform, depth+1)
	case ocispec.MediaTypeImageManifest, dockerManifestV2MediaType:
		data, err := content.FetchAll(ctx, repo, desc)
		if err != nil {
			return 0, fmt.Errorf("fetch image manifest: %w", err)
		}
		var manifest ocispec.Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return 0, fmt.Errorf("decode image manifest: %w", err)
		}
		if manifest.Config.Digest == "" {
			return 0, fmt.Errorf("image manifest has no config descriptor")
		}
		return manifestPayloadSize(manifest)
	default:
		return 0, fmt.Errorf("unsupported image manifest media type %q", desc.MediaType)
	}
}

func selectPlatformManifest(ctx context.Context, repo *remote.Repository, indexDesc ocispec.Descriptor, platform *ocispec.Platform) (ocispec.Descriptor, error) {
	if platform == nil || platform.OS == "" || platform.Architecture == "" {
		manifests, err := fetchIndexManifests(ctx, repo, indexDesc)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		if len(manifests) != 1 {
			return ocispec.Descriptor{}, fmt.Errorf("image index has multiple manifests and device platform is unavailable")
		}
		return manifests[0], nil
	}

	// WithTargetPlatform installs ORAS's manifest selector. Calling MapRoot
	// performs that selection without copying image layer content.
	var opts oras.CopyOptions
	opts.WithTargetPlatform(platform)
	manifest, err := opts.MapRoot(ctx, repo, indexDesc)
	if err == nil {
		return manifest, nil
	}

	// Preserve support for a single-manifest index that omits platform metadata.
	manifests, readErr := fetchIndexManifests(ctx, repo, indexDesc)
	if readErr != nil {
		return ocispec.Descriptor{}, readErr
	}
	if len(manifests) == 1 && manifests[0].Platform == nil {
		return manifests[0], nil
	}
	return ocispec.Descriptor{}, fmt.Errorf("select image manifest for platform %s/%s: %w", platform.OS, platform.Architecture, err)
}

func fetchIndexManifests(ctx context.Context, repo *remote.Repository, indexDesc ocispec.Descriptor) ([]ocispec.Descriptor, error) {
	data, err := content.FetchAll(ctx, repo, indexDesc)
	if err != nil {
		return nil, fmt.Errorf("fetch image index: %w", err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("decode image index: %w", err)
	}
	if len(index.Manifests) == 0 {
		return nil, fmt.Errorf("image index contains no manifests")
	}
	return index.Manifests, nil
}

func manifestPayloadSize(manifest ocispec.Manifest) (int64, error) {
	if manifest.Config.Size < 0 {
		return 0, fmt.Errorf("image manifest has a negative config size")
	}
	total := manifest.Config.Size
	for _, layer := range manifest.Layers {
		if layer.Size < 0 {
			return 0, fmt.Errorf("image manifest has a negative layer size")
		}
		if total > math.MaxInt64-layer.Size {
			return 0, fmt.Errorf("image payload size overflows int64")
		}
		total += layer.Size
	}
	return total, nil
}

func imageDigestCacheKey(orgID uuid.UUID, imageRef string) (string, error) {
	rewritten, err := RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	return "osInspect/" + orgID.String() + "/" + rewritten, nil
}

func CachedImageDigest(ctx context.Context, cache DigestCache, orgID uuid.UUID, image string, resolve func(context.Context) (string, error)) (string, error) {
	dgst, err := DigestFromImageRef(image)
	if err != nil {
		return "", err
	}
	if dgst != "" {
		return dgst, nil
	}
	if cache != nil {
		key, err := imageDigestCacheKey(orgID, image)
		if err != nil {
			return "", err
		}
		raw, err := cache.Get(ctx, key)
		if err != nil {
			return "", err
		}
		if len(raw) > 0 {
			return string(raw), nil
		}
	}
	if resolve == nil {
		return "", fmt.Errorf("resolve image digest for %s: resolver is required", image)
	}
	dgst, err = resolve(ctx)
	if err != nil {
		return "", err
	}
	if dgst == "" {
		return "", fmt.Errorf("resolve image digest for %s: empty digest", image)
	}
	if cache == nil {
		return dgst, nil
	}
	key, err := imageDigestCacheKey(orgID, image)
	if err != nil {
		return "", err
	}
	if _, err := cache.SetNX(ctx, key, []byte(dgst)); err != nil {
		return "", err
	}
	if err := cache.SetExpire(ctx, key, ImageDigestCacheTTL); err != nil {
		return "", err
	}
	return dgst, nil
}
