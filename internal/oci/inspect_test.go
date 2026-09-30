package oci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type digestCache struct {
	values      map[string][]byte
	expirations map[string]time.Duration
	getErr      error
}

const testDigestCacheTTL = 15 * time.Minute

func newDiscardLogger() *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(io.Discard)
	return logger
}

func TestSpecForRegistry(t *testing.T) {
	t.Run("When the system registry is marked insecure it should skip TLS verification", func(t *testing.T) {
		got := specForRegistry("registry.example.com:5000", true)

		require.Equal(t, "registry.example.com:5000", got.Registry)
		require.NotNil(t, got.SkipServerVerification)
		require.True(t, *got.SkipServerVerification)
	})

	t.Run("When no insecure registry setting exists it should retain TLS verification", func(t *testing.T) {
		got := specForRegistry("registry.example.com:5000", false)

		require.Equal(t, "registry.example.com:5000", got.Registry)
		require.Nil(t, got.SkipServerVerification)
	})

	t.Run("When an explicit repository spec matches it should take precedence", func(t *testing.T) {
		skipVerification := false
		explicit := &domain.OciRepoSpec{Registry: "registry.example.com:5000", SkipServerVerification: &skipVerification}

		got := SpecForRegistry("registry.example.com:5000", explicit)

		require.Same(t, explicit, got)
		require.False(t, *got.SkipServerVerification)
	})
}

func (c *digestCache) Get(_ context.Context, key string) ([]byte, error) {
	if c.getErr != nil {
		return nil, c.getErr
	}
	return c.values[key], nil
}

func (c *digestCache) Set(_ context.Context, key string, value []byte, expiration time.Duration) error {
	c.values[key] = append([]byte(nil), value...)
	if c.expirations == nil {
		c.expirations = make(map[string]time.Duration)
	}
	c.expirations[key] = expiration
	return nil
}

func TestCachedImageDigestScopesCacheByOrganization(t *testing.T) {
	cache := &digestCache{values: make(map[string][]byte)}
	image := "quay.io/example/os:latest"
	orgA := uuid.New()
	orgB := uuid.New()

	gotA, err := CachedImageDigest(context.Background(), nil, cache, orgA, image, testDigestCacheTTL, func(context.Context) (string, error) {
		return "sha256:aaa", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:aaa", gotA)

	gotB, err := CachedImageDigest(context.Background(), nil, cache, orgB, image, testDigestCacheTTL, func(context.Context) (string, error) {
		return "sha256:bbb", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:bbb", gotB)
	require.Len(t, cache.values, 2)
}

func TestCachedImageDigestPair(t *testing.T) {
	t.Run("When target reference is a mutable tag it should cache the resolved digest for the TTL", func(t *testing.T) {
		cache := &digestCache{values: make(map[string][]byte)}
		orgID := uuid.New()
		imageRef := "quay.io/example/app:stable"
		indexDigest1 := "sha256:" + strings.Repeat("1", 64)
		indexDigest2 := "sha256:" + strings.Repeat("2", 64)
		currentIndexDigest := indexDigest1
		var (
			digestResolveCalls int
			pairResolveCalls   int
			resolvedRefs       []string
		)
		resolveImageDigest := func(context.Context) (string, error) {
			digestResolveCalls++
			return currentIndexDigest, nil
		}
		resolvePair := func(_ context.Context, resolvedImage string) (ImageDigestPair, error) {
			pairResolveCalls++
			resolvedRefs = append(resolvedRefs, resolvedImage)
			return ImageDigestPair{SourceDigest: "sha256:source", TargetDigest: fmt.Sprintf("sha256:target-%d", pairResolveCalls)}, nil
		}

		first, err := CachedImageDigestPair(context.Background(), nil, cache, orgID, imageRef, "sha256:source", nil, testDigestCacheTTL, resolveImageDigest, resolvePair)
		require.NoError(t, err)
		tagKey, err := imageDigestCacheKey(orgID, imageRef, testDigestCacheTTL)
		require.NoError(t, err)
		require.Equal(t, testDigestCacheTTL, cache.expirations[tagKey])

		// The registry tag moves, but the TTL cache keeps this call pinned to the
		// digest observed by the first lookup.
		currentIndexDigest = indexDigest2
		second, err := CachedImageDigestPair(context.Background(), nil, cache, orgID, imageRef, "sha256:source", nil, testDigestCacheTTL, resolveImageDigest, resolvePair)
		require.NoError(t, err)

		require.Equal(t, "sha256:target-1", first.TargetDigest)
		require.Equal(t, first, second)
		require.Equal(t, 1, digestResolveCalls)
		require.Equal(t, 1, pairResolveCalls)
		require.Equal(t, []string{"quay.io/example/app@" + indexDigest1}, resolvedRefs)

		// Simulate TTL expiration. A fresh registry resolution now produces and
		// caches the pair for the new immutable target digest.
		delete(cache.values, tagKey)
		third, err := CachedImageDigestPair(context.Background(), nil, cache, orgID, imageRef, "sha256:source", nil, testDigestCacheTTL, resolveImageDigest, resolvePair)
		require.NoError(t, err)
		require.Equal(t, "sha256:target-2", third.TargetDigest)
		require.Equal(t, 2, digestResolveCalls)
		require.Equal(t, 2, pairResolveCalls)
		require.Equal(t, []string{"quay.io/example/app@" + indexDigest1, "quay.io/example/app@" + indexDigest2}, resolvedRefs)
	})

	t.Run("When target reference is pinned by digest it should use the cache", func(t *testing.T) {
		cache := &digestCache{values: make(map[string][]byte)}
		orgID := uuid.New()
		targetDigest := "sha256:" + strings.Repeat("a", 64)
		imageRef := "quay.io/example/app@" + targetDigest
		var resolveCalls int
		resolve := func(_ context.Context, resolvedImage string) (ImageDigestPair, error) {
			resolveCalls++
			require.Equal(t, imageRef, resolvedImage)
			return ImageDigestPair{SourceDigest: "sha256:source", TargetDigest: targetDigest}, nil
		}

		first, err := CachedImageDigestPair(context.Background(), nil, cache, orgID, imageRef, "sha256:source", nil, testDigestCacheTTL, nil, resolve)
		require.NoError(t, err)
		second, err := CachedImageDigestPair(context.Background(), nil, cache, orgID, imageRef, "sha256:source", nil, testDigestCacheTTL, nil, resolve)
		require.NoError(t, err)

		require.Equal(t, first, second)
		require.Equal(t, 1, resolveCalls)
		require.Len(t, cache.values, 1)
	})
}

func TestCachedImageDigestPairFallsBackWhenCacheReadsFail(t *testing.T) {
	targetDigest := "sha256:" + strings.Repeat("a", 64)
	cache := &digestCache{values: make(map[string][]byte), getErr: errors.New("cache unavailable")}
	var digestResolveCalls, pairResolveCalls int

	pair, err := CachedImageDigestPair(
		context.Background(),
		newDiscardLogger(),
		cache,
		uuid.New(),
		"quay.io/example/app:stable",
		"sha256:source",
		nil,
		testDigestCacheTTL,
		func(context.Context) (string, error) {
			digestResolveCalls++
			return targetDigest, nil
		},
		func(context.Context, string) (ImageDigestPair, error) {
			pairResolveCalls++
			return ImageDigestPair{SourceDigest: "sha256:source", TargetDigest: targetDigest}, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, "sha256:source", pair.SourceDigest)
	require.Equal(t, targetDigest, pair.TargetDigest)
	require.Equal(t, 1, digestResolveCalls)
	require.Equal(t, 1, pairResolveCalls)
}

func TestCachedImageDigestPairFallsBackWhenCachedPairIsInvalid(t *testing.T) {
	targetDigest := "sha256:" + strings.Repeat("a", 64)
	imageRef := "quay.io/example/app@" + targetDigest
	orgID := uuid.New()
	cache := &digestCache{values: make(map[string][]byte)}
	key, err := imageDigestPairCacheKey(orgID, imageRef, "sha256:source", nil, testDigestCacheTTL)
	require.NoError(t, err)
	cache.values[key] = []byte("not-json")

	var resolveCalls int
	pair, err := CachedImageDigestPair(
		context.Background(),
		newDiscardLogger(),
		cache,
		orgID,
		imageRef,
		"sha256:source",
		nil,
		testDigestCacheTTL,
		nil,
		func(context.Context, string) (ImageDigestPair, error) {
			resolveCalls++
			return ImageDigestPair{SourceDigest: "sha256:source", TargetDigest: targetDigest}, nil
		},
	)

	require.NoError(t, err)
	require.Equal(t, ImageDigestPair{SourceDigest: "sha256:source", TargetDigest: targetDigest}, pair)
	require.Equal(t, 1, resolveCalls)
	var cachedPair ImageDigestPair
	require.NoError(t, json.Unmarshal(cache.values[key], &cachedPair))
	require.Equal(t, pair, cachedPair)
}

type testOCIManifest struct {
	desc ocispec.Descriptor
	data []byte
}

func newTestOCIImageManifest(t *testing.T, name string, configSize int64, layerSizes ...int64) testOCIManifest {
	t.Helper()

	layers := make([]ocispec.Descriptor, len(layerSizes))
	for i, size := range layerSizes {
		layers[i] = ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageLayer,
			Digest:    digest.FromString(fmt.Sprintf("%s-layer-%d", name, i)),
			Size:      size,
		}
	}
	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageConfig,
			Digest:    digest.FromString(name + "-config"),
			Size:      configSize,
		},
		Layers: layers,
	}
	data, err := json.Marshal(manifest)
	require.NoError(t, err)

	return testOCIManifest{
		desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.FromBytes(data),
			Size:      int64(len(data)),
		},
		data: data,
	}
}

func newTestOCIImageIndex(t *testing.T, manifests ...ocispec.Descriptor) testOCIManifest {
	t.Helper()
	index := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: manifests,
	}
	data, err := json.Marshal(index)
	require.NoError(t, err)

	return testOCIManifest{
		desc: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageIndex,
			Digest:    digest.FromBytes(data),
			Size:      int64(len(data)),
		},
		data: data,
	}
}

func newTestManifestRegistry(t *testing.T, manifests map[string]testOCIManifest) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.WriteHeader(http.StatusOK)
			return
		}

		const manifestPath = "/v2/example/app/manifests/"
		if (r.Method != http.MethodHead && r.Method != http.MethodGet) || !strings.HasPrefix(r.URL.Path, manifestPath) {
			http.NotFound(w, r)
			return
		}
		reference := strings.TrimPrefix(r.URL.Path, manifestPath)
		manifest, ok := manifests[reference]
		if !ok {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", manifest.desc.MediaType)
		w.Header().Set("Content-Length", strconv.Itoa(len(manifest.data)))
		w.Header().Set("Docker-Content-Digest", manifest.desc.Digest.String())
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(manifest.data)
	}))
}

func TestInspectImagePayloadSizeUsesTargetDigestAndPlatform(t *testing.T) {
	amd64 := newTestOCIImageManifest(t, "amd64", 10, 100, 200)
	arm64 := newTestOCIImageManifest(t, "arm64", 20, 500)
	tagged := newTestOCIImageManifest(t, "tagged", 2, 3)

	amd64Desc := amd64.desc
	amd64Desc.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	arm64Desc := arm64.desc
	arm64Desc.Platform = &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	index := newTestOCIImageIndex(t, amd64Desc, arm64Desc)

	server := newTestManifestRegistry(t, map[string]testOCIManifest{
		"latest":                   tagged,
		amd64.desc.Digest.String(): amd64,
		arm64.desc.Digest.String(): arm64,
		index.desc.Digest.String(): index,
	})
	t.Cleanup(server.Close)

	registry := strings.TrimPrefix(server.URL, "http://")
	scheme := domain.OciRepoSchemeHttp
	repositorySpec := &domain.OciRepoSpec{Registry: registry, Scheme: &scheme}
	imageRef := registry + "/example/app:latest"

	for _, tt := range []struct {
		name     string
		platform *ocispec.Platform
		wantSize int64
	}{
		{
			name:     "When the platform is amd64 it should return that manifest size",
			platform: &ocispec.Platform{OS: "linux", Architecture: "amd64"},
			wantSize: 310,
		},
		{
			name:     "When the platform is arm64 it should return that manifest size",
			platform: &ocispec.Platform{OS: "linux", Architecture: "arm64"},
			wantSize: 520,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InspectImagePayloadSize(context.Background(), imageRef, index.desc.Digest.String(), repositorySpec, tt.platform)
			require.NoError(t, err)
			require.NotNil(t, got)
			require.Equal(t, tt.wantSize, *got)
		})
	}

	t.Run("When a multi-platform index has no device platform it should return an error", func(t *testing.T) {
		_, err := InspectImagePayloadSize(context.Background(), imageRef, index.desc.Digest.String(), repositorySpec, nil)
		require.ErrorContains(t, err, "device platform is unavailable")
	})

	t.Run("When the target manifest cannot be resolved it should return an error", func(t *testing.T) {
		missingImage := registry + "/example/app:missing"
		_, err := InspectImagePayloadSize(context.Background(), missingImage, "", repositorySpec, &ocispec.Platform{OS: "linux", Architecture: "amd64"})
		require.ErrorContains(t, err, "resolve image manifest")
	})
}
