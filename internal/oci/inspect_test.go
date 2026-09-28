package oci

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/stretchr/testify/require"
)

type digestCache struct {
	values map[string][]byte
}

func (c *digestCache) Get(_ context.Context, key string) ([]byte, error) {
	return c.values[key], nil
}

func (c *digestCache) SetNX(_ context.Context, key string, value []byte) (bool, error) {
	if _, ok := c.values[key]; ok {
		return false, nil
	}
	c.values[key] = append([]byte(nil), value...)
	return true, nil
}

func (c *digestCache) SetExpire(_ context.Context, _ string, _ time.Duration) error {
	return nil
}

func TestCachedImageDigestScopesCacheByOrganization(t *testing.T) {
	cache := &digestCache{values: make(map[string][]byte)}
	image := "quay.io/example/os:latest"
	orgA := uuid.New()
	orgB := uuid.New()

	gotA, err := CachedImageDigest(context.Background(), cache, orgA, image, func(context.Context) (string, error) {
		return "sha256:aaa", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:aaa", gotA)

	gotB, err := CachedImageDigest(context.Background(), cache, orgB, image, func(context.Context) (string, error) {
		return "sha256:bbb", nil
	})
	require.NoError(t, err)
	require.Equal(t, "sha256:bbb", gotB)
	require.Len(t, cache.values, 2)
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
}
