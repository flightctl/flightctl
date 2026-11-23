package appinspection

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

func TestInspectPackageArtifact(t *testing.T) {
	const compose = "services:\n  app:\n    image: quay.io/example/app:base\n"
	const override = "services:\n  app:\n    image: quay.io/example/app:override\n"
	for _, tc := range []struct {
		name           string
		appType        v1beta1.AppType
		files          []archiveEntry
		expectedFormat string
		expectedImages []Reference
	}{
		{
			name: "docker-compose.yaml", appType: v1beta1.AppTypeCompose,
			files: []archiveEntry{{name: "docker-compose.yaml", data: compose}}, expectedFormat: "raw",
			expectedImages: []Reference{{Image: "quay.io/example/app:base", Role: RoleWorkload, ConsumerKind: ConsumerContainer}},
		},
		{
			name: "compose.tar", appType: v1beta1.AppTypeCompose,
			files: []archiveEntry{{name: "docker-compose.yaml", data: compose}}, expectedFormat: "tar",
			expectedImages: []Reference{{Image: "quay.io/example/app:base", Role: RoleWorkload, ConsumerKind: ConsumerContainer}},
		},
		{
			name: "compose.tar.gz", appType: v1beta1.AppTypeCompose,
			files: []archiveEntry{
				{name: "docker-compose.override.yaml", data: override},
				{name: "docker-compose.override.yml", data: "invalid fallback"},
				{name: "podman-compose.yaml", data: "invalid fallback"},
				{name: "docker-compose.yaml", data: compose},
			}, expectedFormat: "tar.gz",
			expectedImages: []Reference{{Image: "quay.io/example/app:override", Role: RoleWorkload, ConsumerKind: ConsumerContainer}},
		},
		{
			name: "quadlet.tgz", appType: v1beta1.AppTypeQuadlet,
			files: []archiveEntry{
				{name: "app.container", data: "[Container]\nImage=quay.io/example/app:base\n"},
				{name: "app.container.d/over.conf", data: "[Container]\nImage=quay.io/example/app:override\n"},
				{name: "pod.kube", data: "[Kube]\nYaml=pods/pod.yaml\n"},
				{name: "pods/pod.yaml", data: "spec:\n  initContainers:\n    - image: quay.io/example/init:latest\n  containers:\n    - image: quay.io/example/pod:latest\n  volumes:\n    - image:\n        reference: quay.io/example/volume:latest\n"},
			}, expectedFormat: "tar.gz",
			expectedImages: []Reference{
				{Image: "quay.io/example/app:override", Role: RoleWorkload, ConsumerKind: ConsumerContainer},
				{Image: "quay.io/example/init:latest", Role: RoleWorkload, ConsumerKind: ConsumerContainer},
				{Image: "quay.io/example/pod:latest", Role: RoleWorkload, ConsumerKind: ConsumerContainer},
				{Image: "quay.io/example/volume:latest", Role: RoleVolume, ConsumerKind: ConsumerContainer},
			},
		},
	} {
		t.Run("When the registry has "+tc.name+" it should inspect the package", func(t *testing.T) {
			data := []byte(tc.files[0].data)
			if tc.expectedFormat != "raw" {
				data = makeTar(t, tc.files...)
			}
			if tc.expectedFormat == "tar.gz" {
				data = gzipBytes(t, data)
			}
			registry := packageRegistry{}
			layer := registry.blob(tc.name, "application/octet-stream", data)
			parent := registry.manifest(t, ocispec.DescriptorEmptyJSON, layer)
			repo := registry.serve(t, parent, true)

			denied := InspectPackage(t.Context(), tc.appType, repo, "target", nil)
			require.Equal(t, []Diagnostic{DiagnosticContentUnavailable}, denied.Diagnostics)
			require.Empty(t, denied.ParentDigest)

			repo.Client = &auth.Client{Credential: auth.StaticCredential(repo.Reference.Registry, auth.Credential{Username: "reader", Password: "token"})}
			result := InspectPackage(t.Context(), tc.appType, repo, "target", nil)
			require.Empty(t, result.Diagnostics)
			require.Equal(t, parent.Digest, result.ParentDigest)
			require.Equal(t, tc.expectedFormat, result.Format)
			require.ElementsMatch(t, tc.expectedImages, result.References)
		})
	}
}

func TestInspectPackageIncompleteArchive(t *testing.T) {
	const base = "services:\n  app:\n    image: quay.io/example/app:known\n"
	data := gzipBytes(t, makeTar(t, archiveEntry{name: "docker-compose.override.yaml", data: "services:\n  app:\n    image: quay.io/example/app:untrusted\n"}))
	badCRC := bytes.Clone(data)
	badCRC[len(badCRC)-8] ^= 1
	for _, tc := range []struct {
		name           string
		data           []byte
		budget         int64
		mismatchDigest bool
		diagnostic     Diagnostic
	}{
		{name: "the new download exceeds the remaining budget", data: data, budget: int64(len(base) + len(data) - 1), diagnostic: DiagnosticBudgetExceeded},
		{name: "files exceed the shared budget", data: data, budget: int64(len(base) + len(data) + 10), diagnostic: DiagnosticBudgetExceeded},
		{name: "gzip checksum fails", data: badCRC, budget: 1 << 20, diagnostic: DiagnosticParseFailed},
		{name: "blob digest differs from the descriptor", data: data, budget: 1 << 20, mismatchDigest: true, diagnostic: DiagnosticContentUnavailable},
	} {
		t.Run("When "+tc.name+" it should retain references from verified files", func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			registry := packageRegistry{}
			known := registry.blob("docker-compose.yaml", "application/octet-stream", []byte(base))
			archive := registry.blob("other.tar.gz", "application/octet-stream", tc.data)
			if tc.mismatchDigest {
				registry["blobs/"+archive.Digest.String()] = packageObject{desc: archive, data: badCRC}
			}
			parent := registry.manifest(t, ocispec.DescriptorEmptyJSON, archive, known)
			repo := registry.serve(t, parent, false)
			result := inspectPackage(t.Context(), v1beta1.AppTypeCompose, repo, "target", nil, tc.budget)
			require.Equal(t, []Diagnostic{tc.diagnostic}, result.Diagnostics)
			require.Equal(t, []Reference{{Image: "quay.io/example/app:known", Role: RoleWorkload, ConsumerKind: ConsumerContainer}}, result.References)
			require.Equal(t, parent.Digest, result.ParentDigest)
			files, err := os.ReadDir(os.TempDir())
			require.NoError(t, err)
			require.Empty(t, files)
		})
	}
}

func TestInspectPackageTarTrailingPadding(t *testing.T) {
	const image = "quay.io/example/app:base"
	const override = "quay.io/example/app:override"
	compose := func(ref string) string { return "services:\n  app:\n    image: " + ref + "\n" }
	for _, tc := range []struct {
		name        string
		padding     int
		corrupt     bool
		diagnostics []Diagnostic
		image       string
	}{
		{name: "padding is at the limit it should discover the override", padding: maxTarTrailingPadding, image: override},
		{name: "padding is over the limit it should retain the base", padding: maxTarTrailingPadding + 1, diagnostics: []Diagnostic{DiagnosticParseFailed}, image: image},
		{name: "the trailer is corrupt at the limit it should retain the base", padding: maxTarTrailingPadding, corrupt: true, diagnostics: []Diagnostic{DiagnosticParseFailed}, image: image},
	} {
		t.Run("When "+tc.name, func(t *testing.T) {
			archive := makeTar(t, archiveEntry{name: "docker-compose.override.yaml", data: compose(override)})
			data := gzipBytes(t, append(archive, make([]byte, tc.padding)...))
			if tc.corrupt {
				data[len(data)-8] ^= 1
			}
			registry := packageRegistry{}
			base := registry.blob("docker-compose.yaml", "application/yaml", []byte(compose(image)))
			layer := registry.blob("package.tgz", "application/gzip", data)
			parent := registry.manifest(t, ocispec.DescriptorEmptyJSON, base, layer)
			repo := registry.serve(t, parent, false)
			result := InspectPackage(t.Context(), v1beta1.AppTypeCompose, repo, "target", nil)
			require.Equal(t, tc.diagnostics, result.Diagnostics)
			require.Equal(t, []Reference{{Image: tc.image, Role: RoleWorkload, ConsumerKind: ConsumerContainer}}, result.References)
		})
	}
}

func TestInspectPackageRejectsInvalidArtifactTitles(t *testing.T) {
	for _, title := range []string{"", ".", "../docker-compose.yaml", "/docker-compose.yaml"} {
		t.Run("When a blob title is invalid it should finish incomplete: "+title, func(t *testing.T) {
			registry := packageRegistry{}
			layer := registry.blob(title, "application/yaml", []byte("services:\n  app:\n    image: images.example.com/org/workload:v2\n"))
			parent := registry.manifest(t, ocispec.DescriptorEmptyJSON, layer)
			repo := registry.serve(t, parent, false)
			result := InspectPackage(t.Context(), v1beta1.AppTypeCompose, repo, "target", nil)
			require.Equal(t, []Diagnostic{DiagnosticParseFailed}, result.Diagnostics)
			require.Equal(t, parent.Digest, result.ParentDigest)
			require.Empty(t, result.References)
		})
	}
}

func TestInspectPackageSelectsTargetAndAppliesImageLayers(t *testing.T) {
	const original = "[Container]\nImage=quay.io/example/app:old\n"
	const updated = "[Container]\nImage=quay.io/example/app:new\n"
	registry := packageRegistry{}
	base := registry.blob("", ocispec.MediaTypeImageLayerGzip, gzipBytes(t, makeTar(t,
		archiveEntry{name: "app.container", data: original},
		archiveEntry{name: "obsolete.container", data: original},
		archiveEntry{name: "nested/obsolete.container", data: original},
	)))
	encoder, err := zstd.NewWriter(nil)
	require.NoError(t, err)
	defer func() { require.NoError(t, encoder.Close()) }()
	overlay := registry.blob("", ocispec.MediaTypeImageLayerZstd, encoder.EncodeAll(makeTar(t,
		archiveEntry{name: "app.container", data: updated},
		archiveEntry{name: ".wh.obsolete.container"},
		archiveEntry{name: "nested/.wh..wh..opq"},
		archiveEntry{name: "nested/current.container", data: updated},
	), nil))
	config := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageConfig}
	wrong := registry.manifest(t, config, base)
	wrong.Platform = &ocispec.Platform{OS: "linux", Architecture: "amd64"}
	right := registry.manifest(t, config, base, overlay)
	right.Platform = &ocispec.Platform{OS: "linux", Architecture: "arm64"}
	index := registry.metadata(t, ocispec.MediaTypeImageIndex, ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2}, Manifests: []ocispec.Descriptor{wrong, right},
	})
	repo := registry.serve(t, index, false)
	result := InspectPackage(t.Context(), v1beta1.AppTypeQuadlet, repo, "target", right.Platform)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, index.Digest, result.ParentDigest)
	require.Equal(t, right.Platform, result.Platform)
	require.Equal(t, "tar", result.Format)
	require.Equal(t, []Reference{{Image: "quay.io/example/app:new", Role: RoleWorkload, ConsumerKind: ConsumerContainer}}, result.References)

	result = InspectPackage(t.Context(), v1beta1.AppTypeQuadlet, repo, "target", nil)
	require.Equal(t, []Diagnostic{DiagnosticContentUnavailable}, result.Diagnostics)
}

func TestInspectPackageCancellation(t *testing.T) {
	const manifestPath = "/v2/example/bundle/manifests/target"
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != manifestPath {
			http.NotFound(w, req)
			return
		}
		cancel()
		<-req.Context().Done()
	}))
	t.Cleanup(srv.Close)
	repo, err := remote.NewRepository(strings.TrimPrefix(srv.URL, "http://") + "/example/bundle")
	require.NoError(t, err)
	repo.PlainHTTP = true
	result := InspectPackage(ctx, v1beta1.AppTypeQuadlet, repo, "target", nil)
	require.Equal(t, []Diagnostic{DiagnosticContentUnavailable}, result.Diagnostics)
	require.Empty(t, result.ParentDigest)
}

type packageObject struct {
	desc ocispec.Descriptor
	data []byte
}

type packageRegistry map[string]packageObject

func (r packageRegistry) blob(title, mediaType string, data []byte) ocispec.Descriptor {
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	r["blobs/"+desc.Digest.String()] = packageObject{desc: desc, data: data}
	if title != "" {
		desc.Annotations = map[string]string{ocispec.AnnotationTitle: title}
	}
	return desc
}

func (r packageRegistry) manifest(t *testing.T, config ocispec.Descriptor, layers ...ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	return r.metadata(t, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, Config: config, Layers: layers,
	})
}

func (r packageRegistry) metadata(t *testing.T, mediaType string, value any) ocispec.Descriptor {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
	r["manifests/"+desc.Digest.String()] = packageObject{desc: desc, data: data}
	return desc
}

func (r packageRegistry) serve(t *testing.T, parent ocispec.Descriptor, private bool) *remote.Repository {
	t.Helper()
	r["manifests/target"] = r["manifests/"+parent.Digest.String()]
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if private {
			user, password, _ := req.BasicAuth()
			if user != "reader" || password != "token" {
				w.Header().Set("WWW-Authenticate", `Basic realm="packages"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		key, match := strings.CutPrefix(req.URL.Path, "/v2/example/bundle/")
		object, ok := r[key]
		if !match || !ok {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", object.desc.MediaType)
		w.Header().Set("Content-Length", strconv.FormatInt(object.desc.Size, 10))
		w.Header().Set("Docker-Content-Digest", object.desc.Digest.String())
		if req.Method == http.MethodGet {
			if _, err := w.Write(object.data); err != nil {
				t.Errorf("writing registry response: %v", err)
			}
		}
	}))
	t.Cleanup(srv.Close)
	repo, err := remote.NewRepository(strings.TrimPrefix(srv.URL, "http://") + "/example/bundle")
	require.NoError(t, err)
	repo.PlainHTTP = true
	return repo
}
