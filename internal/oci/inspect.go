package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

// ErrSourceDigestUnresolved marks failures while resolving or inspecting the
// source digest, allowing delta preparation to skip only that device.
var ErrSourceDigestUnresolved = errors.New("source image digest cannot be resolved")

type DigestCache interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// DeviceImagePlatform returns the OS and architecture reported by a device.
// A missing status or incomplete platform is represented by nil so callers can
// use their existing single-manifest fallback behavior.
func DeviceImagePlatform(device *domain.Device) *ocispec.Platform {
	if device == nil || device.Status == nil {
		return nil
	}
	info := device.Status.SystemInfo
	if info.OperatingSystem == "" || info.Architecture == "" {
		return nil
	}
	return &ocispec.Platform{OS: info.OperatingSystem, Architecture: info.Architecture}
}

func SpecForRegistry(host string, spec *domain.OciRepoSpec) *domain.OciRepoSpec {
	if spec != nil && spec.Registry == host {
		return spec
	}

	registry, err := sysregistriesv2.FindRegistry(nil, host)
	return specForRegistry(host, err == nil && registry != nil && registry.Insecure)
}

func specForRegistry(host string, insecure bool) *domain.OciRepoSpec {
	result := &domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci, Registry: host}
	if insecure {
		skipServerVerification := true
		result.SkipServerVerification = &skipServerVerification
	}
	return result
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

func imageRefWithDigest(imageRef, targetDigest string) (string, error) {
	rewritten, err := RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	named, err := reference.ParseNormalizedNamed(strings.TrimPrefix(rewritten, "docker://"))
	if err != nil {
		return "", err
	}
	dgst, err := digest.Parse(targetDigest)
	if err != nil {
		return "", fmt.Errorf("parse target image digest %q: %w", targetDigest, err)
	}
	pinned, err := reference.WithDigest(reference.TrimNamed(named), dgst)
	if err != nil {
		return "", fmt.Errorf("pin image reference %s to digest %s: %w", imageRef, targetDigest, err)
	}
	return pinned.String(), nil
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

// ImageDigestPair contains the platform-specific source and target manifests
// used for OCI delta generation.
type ImageDigestPair struct {
	SourceDigest string `json:"sourceDigest"`
	TargetDigest string `json:"targetDigest"`
}

// InspectImageDigestPair resolves sourceDigest to its platform-specific image
// manifest, then resolves imageRef to a manifest for the same platform. If the
// source digest is an image index, fallbackPlatform identifies the instance
// selected by the device runtime.
func InspectImageDigestPair(
	ctx context.Context,
	imageRef string,
	sourceDigest string,
	spec *domain.OciRepoSpec,
	fallbackPlatform *ocispec.Platform,
) (ImageDigestPair, error) {
	repo, targetRef, err := RemoteRepository(ctx, spec, imageRef)
	if err != nil {
		return ImageDigestPair{}, err
	}

	sourceDesc, err := repo.Resolve(ctx, sourceDigest)
	if err != nil {
		return ImageDigestPair{}, sourceDigestResolutionError(fmt.Sprintf("resolve source image digest %s", sourceDigest), err)
	}
	sourceManifest, err := resolveImageManifest(ctx, repo, sourceDesc, fallbackPlatform, 0)
	if err != nil {
		return ImageDigestPair{}, sourceDigestResolutionError(fmt.Sprintf("select source image manifest for %s", sourceDigest), err)
	}
	sourcePlatform, err := imageManifestPlatform(ctx, repo, sourceManifest, fallbackPlatform)
	if err != nil {
		return ImageDigestPair{}, sourceDigestResolutionError(fmt.Sprintf("inspect source image platform for %s", sourceDigest), err)
	}

	targetDesc, err := repo.Resolve(ctx, targetRef)
	if err != nil {
		return ImageDigestPair{}, fmt.Errorf("resolve target image %s: %w", imageRef, err)
	}
	targetManifest, _, err := resolveCompatibleTargetManifest(ctx, repo, targetDesc, sourcePlatform, 0)
	if err != nil {
		return ImageDigestPair{}, fmt.Errorf("select target image manifest for %s on platform %+v: %w", imageRef, sourcePlatform, err)
	}

	return ImageDigestPair{
		SourceDigest: sourceManifest.Digest.String(),
		TargetDigest: targetManifest.Digest.String(),
	}, nil
}

func sourceDigestResolutionError(stage string, err error) error {
	return fmt.Errorf("%s: %w: %w", stage, ErrSourceDigestUnresolved, err)
}

func resolveImageManifest(
	ctx context.Context,
	repo *remote.Repository,
	desc ocispec.Descriptor,
	platform *ocispec.Platform,
	depth int,
) (ocispec.Descriptor, error) {
	if depth > maxImageIndexDepth {
		return ocispec.Descriptor{}, fmt.Errorf("image index nesting exceeds %d", maxImageIndexDepth)
	}
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestListMediaType:
		manifest, err := selectPlatformManifest(ctx, repo, desc, platform)
		if err != nil {
			return ocispec.Descriptor{}, err
		}
		return resolveImageManifest(ctx, repo, manifest, platform, depth+1)
	case ocispec.MediaTypeImageManifest, dockerManifestV2MediaType:
		return desc, nil
	default:
		return ocispec.Descriptor{}, fmt.Errorf("unsupported image manifest media type %q", desc.MediaType)
	}
}

func imageManifestPlatform(
	ctx context.Context,
	repo *remote.Repository,
	desc ocispec.Descriptor,
	fallback *ocispec.Platform,
) (*ocispec.Platform, error) {
	data, err := content.FetchAll(ctx, repo, desc)
	if err != nil {
		return nil, fmt.Errorf("fetch image manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("decode image manifest: %w", err)
	}
	if manifest.Config.Digest == "" {
		return nil, fmt.Errorf("image manifest has no config descriptor")
	}
	configData, err := content.FetchAll(ctx, repo, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("fetch image config: %w", err)
	}
	var image ocispec.Image
	if err := json.Unmarshal(configData, &image); err != nil {
		return nil, fmt.Errorf("decode image config: %w", err)
	}
	// The manifest's config describes the actual image. Index metadata is a
	// useful fallback when a config omits platform fields, and the requested
	// platform is the final fallback for images without either source.
	platform := &ocispec.Platform{}
	if desc.Platform != nil {
		*platform = *desc.Platform
	}
	if image.OS != "" {
		platform.OS = image.OS
	}
	if image.Architecture != "" {
		platform.Architecture = image.Architecture
	}
	if image.OSVersion != "" {
		platform.OSVersion = image.OSVersion
	}
	if image.Variant != "" {
		platform.Variant = image.Variant
	}
	if len(image.OSFeatures) > 0 {
		platform.OSFeatures = image.OSFeatures
	}
	if fallback != nil {
		if platform.OS == "" {
			platform.OS = fallback.OS
		}
		if platform.Architecture == "" {
			platform.Architecture = fallback.Architecture
		}
		if platform.Variant == "" {
			platform.Variant = fallback.Variant
		}
		if platform.OSVersion == "" {
			platform.OSVersion = fallback.OSVersion
		}
		if len(platform.OSFeatures) == 0 {
			platform.OSFeatures = fallback.OSFeatures
		}
	}
	if platform.OS == "" || platform.Architecture == "" {
		return nil, fmt.Errorf("image config does not specify an OS and architecture")
	}
	return platform, nil
}

func platformsCompatible(source, target *ocispec.Platform) bool {
	if source == nil || target == nil || source.OS != target.OS || source.Architecture != target.Architecture {
		return false
	}
	return source.Variant == "" || target.Variant == "" || source.Variant == target.Variant
}

// resolveCompatibleTargetManifest selects a target manifest that is compatible
// with the source image platform. Index descriptors often omit optional fields
// that are present in image configs, so selection prioritizes an exact variant
// when available, permits a descriptor with no variant, and validates the
// actual image config before accepting a manifest. OS version and feature
// metadata are not used to select an update target.
func resolveCompatibleTargetManifest(
	ctx context.Context,
	repo *remote.Repository,
	desc ocispec.Descriptor,
	sourcePlatform *ocispec.Platform,
	depth int,
) (ocispec.Descriptor, *ocispec.Platform, error) {
	if depth > maxImageIndexDepth {
		return ocispec.Descriptor{}, nil, fmt.Errorf("image index nesting exceeds %d", maxImageIndexDepth)
	}
	switch desc.MediaType {
	case ocispec.MediaTypeImageIndex, dockerManifestListMediaType:
		candidates, err := compatibleTargetManifestCandidates(ctx, repo, desc, sourcePlatform)
		if err != nil {
			return ocispec.Descriptor{}, nil, err
		}
		if len(candidates) == 0 {
			return ocispec.Descriptor{}, nil, fmt.Errorf("image index has no manifest compatible with platform %s/%s", sourcePlatform.OS, sourcePlatform.Architecture)
		}

		var lastErr error
		for _, candidate := range candidates {
			manifest, platform, err := resolveCompatibleTargetManifest(ctx, repo, candidate, sourcePlatform, depth+1)
			if err == nil {
				return manifest, platform, nil
			}
			lastErr = err
		}
		return ocispec.Descriptor{}, nil, fmt.Errorf("no compatible target manifest: %w", lastErr)
	case ocispec.MediaTypeImageManifest, dockerManifestV2MediaType:
		platform, err := imageManifestPlatform(ctx, repo, desc, nil)
		if err != nil {
			return ocispec.Descriptor{}, nil, fmt.Errorf("inspect target image platform: %w", err)
		}
		if !platformsCompatible(sourcePlatform, platform) {
			return ocispec.Descriptor{}, nil, fmt.Errorf("source platform %+v is incompatible with target platform %+v", sourcePlatform, platform)
		}
		return desc, platform, nil
	default:
		return ocispec.Descriptor{}, nil, fmt.Errorf("unsupported image manifest media type %q", desc.MediaType)
	}
}

func compatibleTargetManifestCandidates(
	ctx context.Context,
	repo *remote.Repository,
	indexDesc ocispec.Descriptor,
	sourcePlatform *ocispec.Platform,
) ([]ocispec.Descriptor, error) {
	manifests, err := fetchIndexManifests(ctx, repo, indexDesc)
	if err != nil {
		return nil, err
	}

	var exactVariant []ocispec.Descriptor
	var compatibleVariant []ocispec.Descriptor
	var platformUnknown []ocispec.Descriptor
	for _, manifest := range manifests {
		platform := manifest.Platform
		if platform == nil || platform.OS == "" || platform.Architecture == "" {
			platformUnknown = append(platformUnknown, manifest)
			continue
		}
		if platform.OS != sourcePlatform.OS || platform.Architecture != sourcePlatform.Architecture {
			continue
		}
		if sourcePlatform.Variant != "" && platform.Variant != "" && platform.Variant != sourcePlatform.Variant {
			continue
		}
		if sourcePlatform.Variant != "" && platform.Variant == sourcePlatform.Variant {
			exactVariant = append(exactVariant, manifest)
		} else {
			compatibleVariant = append(compatibleVariant, manifest)
		}
	}

	candidates := make([]ocispec.Descriptor, 0, len(exactVariant)+len(compatibleVariant)+len(platformUnknown))
	candidates = append(candidates, exactVariant...)
	candidates = append(candidates, compatibleVariant...)
	candidates = append(candidates, platformUnknown...)
	return candidates, nil
}

// CachedImageDigestPair resolves mutable target references through the
// short-lived image digest cache, then caches the pair by immutable target
// digest. The tag lookup can be stale for at most cacheTTL. The pair cache key
// also includes the source digest and fallback platform because a
// multi-platform target can resolve to a different leaf digest for different
// source platforms.
func CachedImageDigestPair(
	ctx context.Context,
	logger logrus.FieldLogger,
	cache DigestCache,
	orgID uuid.UUID,
	imageRef string,
	sourceDigest string,
	fallbackPlatform *ocispec.Platform,
	cacheTTL time.Duration,
	resolveImageDigest func(context.Context) (string, error),
	resolvePair func(context.Context, string) (ImageDigestPair, error),
) (ImageDigestPair, error) {
	if sourceDigest == "" {
		return ImageDigestPair{}, fmt.Errorf("resolve image digest pair for %s: source digest is required", imageRef)
	}
	targetDigest, err := DigestFromImageRef(imageRef)
	if err != nil {
		return ImageDigestPair{}, err
	}
	if targetDigest == "" {
		targetDigest, err = CachedImageDigest(ctx, logger, cache, orgID, imageRef, cacheTTL, resolveImageDigest)
		if err != nil {
			return ImageDigestPair{}, err
		}
	}
	resolvedImageRef, err := imageRefWithDigest(imageRef, targetDigest)
	if err != nil {
		return ImageDigestPair{}, err
	}
	useCache := cache != nil && cacheTTL > 0
	var key string
	if useCache {
		key, err = imageDigestPairCacheKey(orgID, resolvedImageRef, sourceDigest, fallbackPlatform, cacheTTL)
		if err != nil {
			return ImageDigestPair{}, err
		}
		raw, cacheErr := cache.Get(ctx, key)
		if cacheErr != nil {
			if logger != nil {
				logger.WithError(cacheErr).Warn("failed reading OCI image digest pair cache; resolving image digests")
			}
		} else if len(raw) > 0 {
			var pair ImageDigestPair
			if err := json.Unmarshal(raw, &pair); err != nil {
				if logger != nil {
					logger.WithError(err).Warn("invalid OCI image digest pair cache entry; resolving image digests")
				}
			} else if pair.SourceDigest != "" && pair.TargetDigest != "" {
				return pair, nil
			}
		}
	}
	if resolvePair == nil {
		return ImageDigestPair{}, fmt.Errorf("resolve image digest pair for %s: resolver is required", imageRef)
	}
	pair, err := resolvePair(ctx, resolvedImageRef)
	if err != nil {
		return ImageDigestPair{}, err
	}
	if pair.SourceDigest == "" || pair.TargetDigest == "" {
		return ImageDigestPair{}, fmt.Errorf("resolve image digest pair for %s: empty source or target digest", imageRef)
	}
	if useCache {
		raw, err := json.Marshal(pair)
		if err != nil {
			return ImageDigestPair{}, fmt.Errorf("encode image digest pair: %w", err)
		}
		writeDigestCache(ctx, cache, key, raw, cacheTTL)
	}
	return pair, nil
}

func writeDigestCache(ctx context.Context, cache DigestCache, key string, value []byte, ttl time.Duration) {
	if cache == nil || ttl <= 0 {
		return
	}
	cacheWithTTL, ok := cache.(interface {
		Set(context.Context, string, []byte, time.Duration) error
	})
	if !ok {
		// A cache implementation without atomic set-with-TTL support is safe to
		// read from, but should not receive entries that could outlive their TTL.
		return
	}
	// Cache writes are an optimization. Do not fail a valid image inspection
	// because the cache is temporarily unavailable.
	_ = cacheWithTTL.Set(ctx, key, value, ttl)
}

func imageDigestPairCacheKey(orgID uuid.UUID, imageRef, sourceDigest string, platform *ocispec.Platform, ttl time.Duration) (string, error) {
	key, err := imageDigestCacheKey(orgID, imageRef, ttl)
	if err != nil {
		return "", err
	}
	platformJSON, err := json.Marshal(platform)
	if err != nil {
		return "", fmt.Errorf("encode image platform cache key: %w", err)
	}
	platformHash := sha256.Sum256(platformJSON)
	return key + "/source/" + sourceDigest + "/platform/" + hex.EncodeToString(platformHash[:]), nil
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

func imageDigestCacheKey(orgID uuid.UUID, imageRef string, ttl time.Duration) (string, error) {
	rewritten, err := RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	return "ociDigest/v2/" + orgID.String() + "/ttl/" + strconv.FormatInt(ttl.Nanoseconds(), 10) + "/" + rewritten, nil
}

func CachedImageDigest(ctx context.Context, logger logrus.FieldLogger, cache DigestCache, orgID uuid.UUID, image string, cacheTTL time.Duration, resolve func(context.Context) (string, error)) (string, error) {
	dgst, err := DigestFromImageRef(image)
	if err != nil {
		return "", err
	}
	if dgst != "" {
		return dgst, nil
	}
	useCache := cache != nil && cacheTTL > 0
	var key string
	if useCache {
		key, err = imageDigestCacheKey(orgID, image, cacheTTL)
		if err != nil {
			return "", err
		}
		raw, cacheErr := cache.Get(ctx, key)
		if cacheErr != nil {
			if logger != nil {
				logger.WithError(cacheErr).Warn("failed reading OCI image digest cache; resolving image reference")
			}
		} else if len(raw) > 0 {
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
	if !useCache {
		return dgst, nil
	}
	writeDigestCache(ctx, cache, key, []byte(dgst), cacheTTL)
	return dgst, nil
}
