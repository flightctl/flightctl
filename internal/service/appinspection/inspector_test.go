package appinspection

import (
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"oras.land/oras-go/v2/registry"
)

func TestResolvePackageRepositoryAccess(t *testing.T) {
	const host = "quay.example.com"
	const path = "org/apps/package"
	for _, tc := range []struct {
		name      string
		first     []domain.OciRepoSpec
		second    []domain.OciRepoSpec
		selected  *domain.OciRepoSpec
		ambiguous bool
	}{
		{
			name: "multiple scopes match it should select the exact repository from the second page",
			first: []domain.OciRepoSpec{
				{Registry: host, Namespace: lo.ToPtr("org/apps")},
				{Registry: host, Namespace: lo.ToPtr("org/apps"), Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)},
				{Registry: "another.example.com", Repository: lo.ToPtr(path)},
			},
			second:   []domain.OciRepoSpec{{Registry: host, Repository: lo.ToPtr(path)}},
			selected: &domain.OciRepoSpec{Registry: host, Repository: lo.ToPtr(path)},
		},
		{
			name:  "the namespace shares only a partial path component it should use anonymous access",
			first: []domain.OciRepoSpec{{Registry: host, Namespace: lo.ToPtr("org/app")}},
		},
		{
			name: "equally scoped results conflict it should refuse the lookup",
			first: []domain.OciRepoSpec{
				{Registry: host, Namespace: lo.ToPtr("org")},
				{Registry: host, Namespace: lo.ToPtr("org"), Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)},
			},
			ambiguous: true,
		},
	} {
		t.Run("When "+tc.name, func(t *testing.T) {
			orgID := uuid.New()
			service := repository.NewMockService(gomock.NewController(t))
			page := repositoryAccessPage(t, tc.first)
			var token *string
			if tc.second != nil {
				token = lo.ToPtr("next-page")
				page.Metadata.Continue = token
			}
			first := service.EXPECT().ListRepositories(gomock.Any(), orgID, domain.ListRepositoriesParams{Limit: lo.ToPtr(int32(1000))}).
				Return(page, domain.Status{Code: http.StatusOK})
			if token != nil {
				service.EXPECT().ListRepositories(gomock.Any(), orgID, domain.ListRepositoriesParams{Limit: lo.ToPtr(int32(1000)), Continue: token}).
					After(first).Return(repositoryAccessPage(t, tc.second), domain.Status{Code: http.StatusOK})
			}
			inspector, err := NewInspector(service)
			require.NoError(t, err)
			spec, err := inspector.resolveAccess(t.Context(), orgID, registry.Reference{Registry: host, Repository: path, Reference: "latest"})
			if tc.ambiguous {
				require.Error(t, err)
				require.Nil(t, spec)
				return
			}
			require.NoError(t, err)
			if tc.selected == nil {
				require.Nil(t, spec)
			} else {
				require.Equal(t, tc.selected.Registry, spec.Registry)
				require.Equal(t, tc.selected.Repository, spec.Repository)
			}
		})
	}
}

func TestInspectPackageLookupFailureRetainsDeclaredReferences(t *testing.T) {
	var app domain.ApplicationProviderSpec
	const image = "quay.example.com/org/compose:v1"
	require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"`+image+`"}`), &app))
	orgID := uuid.New()
	service := repository.NewMockService(gomock.NewController(t))
	service.EXPECT().ListRepositories(gomock.Any(), orgID, gomock.Any()).Return(nil, domain.Status{
		Code: http.StatusForbidden, Message: "sensitive access error",
	})
	inspector, err := NewInspector(service)
	require.NoError(t, err)
	result := inspector.InspectPackage(t.Context(), orgID, &app, nil)
	require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable}, result.Diagnostics)
	require.Equal(t, []appinspection.Reference{{Image: image, Role: appinspection.RolePackage, ConsumerKind: appinspection.ConsumerContainer}}, result.References)
	require.Empty(t, result.ParentDigest)
}

func TestInspectPackageRetriesAfterServiceCredentialsAreRepaired(t *testing.T) {
	const repoPath = "org/compose"
	blob := []byte("services:\n  app:\n    image: images.example.com/org/workload:v2\n")
	blobDigest := digest.FromBytes(blob)
	manifest, err := json.Marshal(ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2}, Config: ocispec.DescriptorEmptyJSON,
		Layers: []ocispec.Descriptor{{
			MediaType: "application/yaml", Digest: blobDigest, Size: int64(len(blob)),
			Annotations: map[string]string{ocispec.AnnotationTitle: "docker-compose.yaml"},
		}},
	})
	require.NoError(t, err)
	manifestDigest := digest.FromBytes(manifest)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet && req.Method != http.MethodHead {
			t.Errorf("inspection must only read registry content; got %s", req.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		user, token, _ := req.BasicAuth()
		if user != "reader" || token != "repaired-test-token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="bundle"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/v2/" + repoPath + "/manifests/v1", "/v2/" + repoPath + "/manifests/" + manifestDigest.String():
			w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
			w.Header().Set("Content-Length", strconv.Itoa(len(manifest)))
			if req.Method == http.MethodGet {
				if _, err := w.Write(manifest); err != nil {
					t.Errorf("writing registry manifest: %v", err)
				}
			}
		case "/v2/" + repoPath + "/blobs/" + blobDigest.String():
			w.Header().Set("Docker-Content-Digest", blobDigest.String())
			w.Header().Set("Content-Length", strconv.Itoa(len(blob)))
			if _, err := w.Write(blob); err != nil {
				t.Errorf("writing registry blob: %v", err)
			}
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(srv.Close)

	key, err := crypto.GenerateAES256Key()
	require.NoError(t, err)
	keyPath := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(keyPath, []byte(key), 0o600))
	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		ActiveKeyID: "test", Keys: []config.EncryptionKeyConfig{{ID: "test", Path: keyPath}},
	}
	require.NoError(t, encryption.InitGlobalEncryption(logrus.New(), cfg))
	ctx := t.Context()
	access := func(token string) *domain.OciAuth {
		t.Helper()
		ciphertext, err := encryption.Encrypt(ctx, encryption.Plaintext(token))
		require.NoError(t, err)
		var auth domain.OciAuth
		require.NoError(t, auth.FromDockerAuth(domain.DockerAuth{Username: "reader", Password: string(ciphertext)}))
		return &auth
	}

	host := strings.TrimPrefix(srv.URL, "https://")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	exact := domain.OciRepoSpec{Registry: host, Repository: lo.ToPtr(repoPath), CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(ca))}
	exact.OciAuth = access("stale-test-token")
	broad := domain.OciRepoSpec{Registry: host, Namespace: lo.ToPtr("org"), OciAuth: access("different-test-token")}
	first := repositoryAccessPage(t, []domain.OciRepoSpec{broad, exact})
	exact.OciAuth = access("repaired-test-token")
	second := repositoryAccessPage(t, []domain.OciRepoSpec{exact, broad})
	var malformed domain.Repository
	require.NoError(t, json.Unmarshal([]byte(`{"type":"oci","registry":false}`), &malformed.Spec))
	second.Items = append(second.Items, malformed)
	third := repositoryAccessPage(t, []domain.OciRepoSpec{exact, broad})
	var httpRepo domain.Repository
	require.NoError(t, json.Unmarshal([]byte(`{"type":"http","registry":"`+host+`","repository":"`+repoPath+`"}`), &httpRepo.Spec))
	third.Items = append(third.Items, httpRepo)

	orgID := uuid.New()
	service := repository.NewMockService(gomock.NewController(t))
	gomock.InOrder(
		service.EXPECT().ListRepositories(gomock.Any(), orgID, gomock.Any()).Return(first, domain.StatusOK()),
		service.EXPECT().ListRepositories(gomock.Any(), orgID, gomock.Any()).Return(second, domain.StatusOK()),
		service.EXPECT().ListRepositories(gomock.Any(), orgID, gomock.Any()).Return(third, domain.StatusOK()),
	)
	inspector, err := NewInspector(service)
	require.NoError(t, err)
	var app domain.ApplicationProviderSpec
	image := host + "/" + repoPath + ":v1"
	require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"`+image+`"}`), &app))
	declared := appinspection.Reference{Image: image, Role: appinspection.RolePackage, ConsumerKind: appinspection.ConsumerContainer}
	denied := inspector.InspectPackage(ctx, orgID, &app, nil)
	require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticContentUnavailable}, denied.Diagnostics)
	require.Equal(t, []appinspection.Reference{declared}, denied.References)
	require.Empty(t, denied.ParentDigest)

	invalidAccess := inspector.InspectPackage(ctx, orgID, &app, nil)
	require.Equal(t, denied, invalidAccess)

	result := inspector.InspectPackage(ctx, orgID, &app, nil)
	require.Empty(t, result.Diagnostics)
	require.Equal(t, manifestDigest, result.ParentDigest)
	require.ElementsMatch(t, []appinspection.Reference{
		declared, {Image: "images.example.com/org/workload:v2", Role: appinspection.RoleWorkload, ConsumerKind: appinspection.ConsumerContainer},
	}, result.References)
}

func repositoryAccessPage(t *testing.T, specs []domain.OciRepoSpec) *domain.RepositoryList {
	t.Helper()
	page := &domain.RepositoryList{}
	for _, spec := range specs {
		spec.Type = domain.OciRepoSpecTypeOci
		var item domain.Repository
		require.NoError(t, item.Spec.FromOciRepoSpec(spec))
		page.Items = append(page.Items, item)
	}
	return page
}
