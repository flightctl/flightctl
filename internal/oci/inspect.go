package oci

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/domain"
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

func imageDigestCacheKey(imageRef string) (string, error) {
	rewritten, err := RewriteImageRef(imageRef)
	if err != nil {
		return "", err
	}
	return "osInspect/" + rewritten, nil
}

func CachedImageDigest(ctx context.Context, cache DigestCache, image string, resolve func(context.Context) (string, error)) (string, error) {
	dgst, err := DigestFromImageRef(image)
	if err != nil {
		return "", err
	}
	if dgst != "" {
		return dgst, nil
	}
	if cache != nil {
		key, err := imageDigestCacheKey(image)
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
	key, err := imageDigestCacheKey(image)
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
