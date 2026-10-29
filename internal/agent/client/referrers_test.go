package client

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

func TestListRegistryReferrers(t *testing.T) {
	const subject = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const candidate = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const source = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	index := ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
		Manifests: []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageManifest, Digest: digest.Digest(candidate), Size: 100,
			ArtifactType: "application/vnd.io.github.containers.oci-delta.v1",
			Annotations:  map[string]string{"io.github.containers.delta.source": source},
		}},
	}
	index.SchemaVersion = 2
	body, err := json.Marshal(index)
	require.NoError(t, err)
	tests := []struct {
		name      string
		apiStatus int
		tagStatus int
		paginate  bool
		auth      bool
		malformed bool
		blocked   bool
		withoutCA bool
		plainHTTP bool
		mirror    bool
		canceled  bool
		wantError bool
		wantCount int
		wantPaths []string
	}{
		{
			name: "When the native API supports pagination it should collect every page", apiStatus: http.StatusOK, paginate: true, wantCount: 2,
			wantPaths: []string{"/v2/test/image/referrers/" + subject, "/v2/test/image/referrers/" + subject + "?next=2"},
		},
		{
			name: "When the native API returns 404 it should fetch the referrers tag index", apiStatus: http.StatusNotFound, tagStatus: http.StatusOK, wantCount: 1,
			wantPaths: []string{"/v2/test/image/referrers/" + subject, "/v2/test/image/manifests/sha256-" + strings.TrimPrefix(subject, "sha256:")},
		},
		{
			name: "When neither API nor index exists it should return an empty list", apiStatus: http.StatusNotFound, tagStatus: http.StatusNotFound,
			wantPaths: []string{"/v2/test/image/referrers/" + subject, "/v2/test/image/manifests/sha256-" + strings.TrimPrefix(subject, "sha256:")},
		},
		{
			name: "When the API denies access it should not bypass it with the tag index", apiStatus: http.StatusForbidden, wantError: true,
			wantPaths: []string{"/v2/test/image/referrers/" + subject},
		},
		{
			name: "When the API returns a server error it should return the failure", apiStatus: http.StatusInternalServerError, wantError: true,
			wantPaths: []string{"/v2/test/image/referrers/" + subject},
		},
		{
			name: "When a registry requires authentication it should use the explicit pull secret", apiStatus: http.StatusOK, auth: true, wantCount: 1,
			wantPaths: []string{"/v2/test/image/referrers/" + subject, "/v2/test/image/referrers/" + subject},
		},
		{
			name: "When the API returns malformed JSON it should reject the response", apiStatus: http.StatusOK, malformed: true, wantError: true,
			wantPaths: []string{"/v2/test/image/referrers/" + subject},
		},
		{name: "When registries configuration blocks the image it should make no request", blocked: true, wantError: true},
		{name: "When the registry CA is not trusted it should reject TLS", apiStatus: http.StatusOK, withoutCA: true, wantError: true},
		{
			name: "When the registry is explicitly insecure it should allow plain HTTP", apiStatus: http.StatusOK, plainHTTP: true, wantCount: 1,
			wantPaths: []string{"/v2/test/image/referrers/" + subject},
		},
		{
			name: "When a mirror fails it should discover through the primary endpoint", apiStatus: http.StatusOK, mirror: true, wantCount: 1,
			wantPaths: []string{"/v2/mirror/image/referrers/" + subject, "/v2/test/image/referrers/" + subject},
		},
		{name: "When discovery is canceled it should stop without a registry request", canceled: true, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			var paths []string
			var pathsMu sync.Mutex
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				pathsMu.Lock()
				paths = append(paths, r.URL.RequestURI())
				pathsMu.Unlock()
				if strings.HasPrefix(r.URL.Path, "/v2/mirror/") {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if tc.auth {
					user, password, ok := r.BasicAuth()
					if !ok || user != "test-user" || password != "test-password" {
						w.Header().Set("WWW-Authenticate", `Basic realm="test-registry"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				}
				status := tc.apiStatus
				if strings.Contains(r.URL.Path, "/manifests/") {
					status = tc.tagStatus
					if status == 0 {
						status = http.StatusNotFound
					}
				}
				if status != http.StatusOK {
					w.WriteHeader(status)
					return
				}
				if tc.paginate && r.URL.RawQuery == "" {
					w.Header().Set("Link", "<"+r.URL.Path+"?next=2>; rel=\"next\"")
				}
				w.Header().Set("Content-Type", ocispec.MediaTypeImageIndex)
				w.Header().Set("Docker-Content-Digest", digest.FromBytes(body).String())
				if tc.malformed {
					_, _ = w.Write([]byte("invalid JSON"))
					return
				}
				_, _ = w.Write(body)
			})
			var server *httptest.Server
			if tc.plainHTTP {
				server = httptest.NewServer(handler)
			} else {
				server = httptest.NewTLSServer(handler)
			}
			defer server.Close()
			host := strings.TrimPrefix(strings.TrimPrefix(server.URL, "https://"), "http://")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			require.NoError(rw.MkdirAll("/etc/containers", 0755))
			// Every successful request also proves registry prefix rewriting: the
			// logical name is deliberately unreachable without this configuration.
			registryConfig := fmt.Sprintf("[[registry]]\nprefix = \"logical.invalid/team\"\nlocation = %q\nblocked = %t\ninsecure = %t\n", host+"/test", tc.blocked, tc.plainHTTP)
			if tc.mirror {
				registryConfig += fmt.Sprintf("[[registry.mirror]]\nlocation = %q\n", host+"/mirror")
			}
			require.NoError(rw.WriteFile("/etc/containers/registries.conf", []byte(registryConfig), 0600))
			if !tc.plainHTTP && !tc.withoutCA {
				certDir := filepath.Join("/etc/containers/certs.d", host)
				require.NoError(rw.MkdirAll(certDir, 0755))
				require.NoError(rw.WriteFile(filepath.Join(certDir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0600))
			}
			var opts []ClientOption
			if tc.auth {
				authJSON := fmt.Sprintf(`{"auths":{%q:{"auth":%q}}}`, host+"/test", base64.StdEncoding.EncodeToString([]byte("test-user:test-password")))
				require.NoError(rw.WriteFile("/auth.json", []byte(authJSON), 0600))
				opts = append(opts, WithPullSecret("/auth.json"))
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			got, err := ListRegistryReferrers(ctx, rw, "logical.invalid/team/image@"+subject, opts...)
			if tc.wantError {
				require.Error(err)
				require.Nil(got)
			} else {
				require.NoError(err)
				require.Len(got.Manifests, tc.wantCount)
				for _, ref := range got.Manifests {
					require.Equal(candidate, ref.Digest)
					require.Equal(source, ref.Annotations["io.github.containers.delta.source"])
					require.Equal(index.Manifests[0].ArtifactType, ref.ArtifactType)
				}
			}
			pathsMu.Lock()
			observedPaths := append([]string(nil), paths...)
			pathsMu.Unlock()
			require.Equal(tc.wantPaths, observedPaths)
		})
	}
}

func TestListRegistryReferrersRejectsUnpinnedSubject(t *testing.T) {
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	_, err := ListRegistryReferrers(context.Background(), rw, "registry.invalid/image:latest")
	require.ErrorContains(t, err, "must include a manifest digest")
}

func TestRegistryTLSConfigLoadsClientCertificates(t *testing.T) {
	require := require.New(t)
	server := httptest.NewTLSServer(http.NotFoundHandler())
	defer server.Close()
	cert := server.TLS.Certificates[0]
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	require.NoError(err)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	dir := "/etc/containers/certs.d/registry.invalid"
	require.NoError(rw.MkdirAll(dir, 0755))
	require.NoError(rw.WriteFile(dir+"/client.cert", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), 0600))
	require.NoError(rw.WriteFile(dir+"/client.key", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0600))
	config, err := registryTLSConfig(rw, "registry.invalid", false)
	require.NoError(err)
	require.Len(config.Certificates, 1)
	require.False(config.InsecureSkipVerify)
}
