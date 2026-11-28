package appinspection

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/repository"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPreparedPackagePinsBlobs(t *testing.T) {
	bundle := func(image string) (string, []byte) {
		t.Helper()
		blob := "services:\n  web:\n    image: " + image + "\n"
		manifest, err := json.Marshal(ocispec.Manifest{
			Versioned: specs.Versioned{SchemaVersion: 2}, Config: ocispec.DescriptorEmptyJSON,
			Layers: []ocispec.Descriptor{{MediaType: "application/yaml", Digest: digest.FromString(blob), Size: int64(len(blob)),
				Annotations: map[string]string{ocispec.AnnotationTitle: "docker-compose.yaml"}}},
		})
		require.NoError(t, err)
		return blob, manifest
	}
	oldBlob, oldManifest := bundle("registry.example/web:v1")
	newBlob, newManifest := bundle("registry.example/web:v2")
	oldDigest, newDigest := digest.FromBytes(oldManifest), digest.FromBytes(newManifest)
	var updated atomic.Bool
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ref, _ := strings.Cut(r.URL.Path, "/manifests/")
		if ref != "" {
			manifest := oldManifest
			if ref == newDigest.String() || (ref == "v1" && updated.Load()) {
				manifest = newManifest
			}
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", digest.FromBytes(manifest).String())
			w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
			if r.Method == http.MethodGet {
				if _, err := w.Write(manifest); err != nil {
					t.Errorf("writing registry manifest: %v", err)
				}
			}
			return
		}
		_, ref, _ = strings.Cut(r.URL.Path, "/blobs/")
		for _, blob := range []string{oldBlob, newBlob} {
			if ref == digest.FromString(blob).String() {
				downloads.Add(1)
				w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
				if _, err := w.Write([]byte(blob)); err != nil {
					t.Errorf("writing registry blob: %v", err)
				}
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	org := uuid.New()
	host := strings.TrimPrefix(srv.URL, "http://")
	repo := repository.NewMockService(gomock.NewController(t))
	repo.EXPECT().ListRepositories(gomock.Any(), org, gomock.Any()).Return(repositoryAccessPage(t, []domain.OciRepoSpec{{Registry: host, Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)}}), domain.StatusOK()).Times(2)
	inspector, err := NewInspector(repo)
	require.NoError(t, err)
	resolver := NewInputResolver(inspector, nil, nil, config.NewDefault(), nil)
	var app EffectiveApplication
	require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"`+host+`/team/package:v1"}`), &app.Spec))
	prepared, code := resolver.prepare(t.Context(), org, app, "")
	require.Empty(t, code)
	require.Equal(t, oldDigest, prepared.identity.ParentDigest)
	require.Zero(t, downloads.Load(), "resolving a lookup key must not extract layers")
	updated.Store(true)
	attempt := prepared.inspect(t.Context(), &app.Spec)
	require.Equal(t, model.InspectionComplete, attempt.State)
	require.Contains(t, attempt.References, inspectionstore.ReferenceInput{Reference: "registry.example/web:v1", Role: model.InspectionRoleWorkload, Mechanism: model.InspectionPullContainer})
	refreshed, code := resolver.prepare(t.Context(), org, app, "")
	require.Empty(t, code)
	require.Equal(t, newDigest, refreshed.identity.ParentDigest)
	require.EqualValues(t, 1, downloads.Load())
}

func TestDeclaredPackageReferenceIdentity(t *testing.T) {
	for _, prefix := range []string{"", "docker://", "oci://"} {
		var app domain.ApplicationProviderSpec
		require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"`+prefix+`registry.example/team/package:v1"}`), &app))
		require.Equal(t, []inspectionstore.ReferenceInput{{
			Reference: "registry.example/team/package:v1", Role: model.InspectionRolePackage, Mechanism: model.InspectionPullContainer,
		}}, DeclaredReferences(&app), "transport %q", prefix)
	}
}

func TestHelmInspectionIdentity(t *testing.T) {
	resolver := NewInputResolver(nil, nil, nil, config.NewDefault(), nil)
	org := uuid.New()
	key := func(values, config string) inspectionstore.InputKey {
		t.Helper()
		app := EffectiveApplication{}
		require.NoError(t, json.Unmarshal([]byte(`{"appType":"helm","name":"demo","image":"oci://registry.example/team/chart:v1","values":`+values+`}`), &app.Spec))
		var err error
		app.RenderedConfig, err = json.Marshal([]domain.FileSpec{{Path: "/root/.config/helm/repositories.yaml", Content: config}})
		require.NoError(t, err)
		prepared, code := resolver.prepare(t.Context(), org, app, "")
		require.Empty(t, code)
		key, err := inspectionstore.KeyForInput(org, prepared.identity)
		require.NoError(t, err)
		return key
	}
	const chartURL = "https://charts.example/charts"
	const source = "repositories:\n- name: child\n  url: " + chartURL + "\n"
	base := key(`{"enabled":true}`, source)
	u, err := url.Parse(chartURL)
	require.NoError(t, err)
	u.User = url.UserPassword("reader", "placeholder")
	require.Equal(t, base, key(`{"enabled":true}`, strings.ReplaceAll(source, chartURL, u.String())))
	require.NotEqual(t, base, key(`{"enabled":false}`, source))
	require.NotEqual(t, base, key(`{"enabled":true}`, strings.ReplaceAll(source, "charts.example", "other.example")))
}
