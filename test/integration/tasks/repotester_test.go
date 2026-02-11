//nolint:gosec
package tasks_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"encoding/pem"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/gliderlabs/ssh"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

// registryHost extracts the host:port from a test server URL
func registryHost(serverURL string) string {
	u, _ := url.Parse(serverURL)
	return u.Host
}

func newOciAuth(username, password string) *api.OciAuth {
	auth := &api.OciAuth{}
	_ = auth.FromDockerAuth(api.DockerAuth{
		Username: username,
		Password: password,
	})
	return auth
}

func startHttpsMTLSRepo(tlsConfig *tls.Config, require *require.Assertions) {
	server := http.Server{
		Addr: "localhost:4443",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			path, err := filepath.Abs("./git/base")
			require.NoError(err)
			indexData, err := os.ReadFile(path)
			require.NoError(err)
			_, err = w.Write(indexData)
			require.NoError(err)
		}),
		TLSConfig: tlsConfig,
	}
	log.Fatal(server.ListenAndServeTLS("", ""))
}

func TestHttpsMTLSRepo(t *testing.T) {
	ctx := context.Background()

	require := require.New(t)

	testDirPath := t.TempDir()
	cfg := ca.NewDefault(testDirPath)
	ca, _, err := crypto.EnsureCA(cfg)
	require.NoError(err)

	serverCerts, _, err := ca.EnsureServerCertificate(ctx, filepath.Join(testDirPath, "server.crt"), filepath.Join(testDirPath, "server.key"), []string{"localhost"}, 1)
	require.NoError(err)

	adminCert, _, err := ca.EnsureClientCertificate(ctx, filepath.Join(testDirPath, "client.crt"), filepath.Join(testDirPath, "client.key"), cfg.AdminCommonName, 1)
	require.NoError(err)

	_, tlsConfig, err := crypto.TLSConfigForServer(ca.GetCABundleX509(), serverCerts)
	require.NoError(err)

	go startHttpsMTLSRepo(tlsConfig, require)
	repotester := tasks.GitRepoTester{}

	clientCertPEM, clientKeyPEM, err := adminCert.GetPEMBytes()
	require.NoError(err)
	caCertPEM, err := ca.GetCABundle()
	require.NoError(err)

	clientCrtB64 := b64.StdEncoding.EncodeToString(clientCertPEM)
	clientKeyB64 := b64.StdEncoding.EncodeToString(clientKeyPEM)
	caB64 := b64.StdEncoding.EncodeToString(caCertPEM)

	spec := api.RepositorySpec{}
	err = spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url:  "https://localhost:4443",
		Type: api.HttpRepoSpecTypeHttp,
		HttpConfig: &api.HttpConfig{
			TlsKey: &clientKeyB64,
			TlsCrt: &clientCrtB64,
			CaCrt:  &caB64,
		}})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("name")}, Spec: spec})

	require.NoError(err)
}

func startSshGitRepo(pubKey gossh.PublicKey, require *require.Assertions) {
	ssh.Handle(func(s ssh.Session) {
		path, err := filepath.Abs("./git/base")
		require.NoError(err)
		indexData, err := os.ReadFile(path)
		require.NoError(err)
		_, err = s.Write(indexData)
		require.NoError(err)
		<-s.Context().Done()
	})

	publicKeyOption := ssh.PublicKeyAuth(func(ctx ssh.Context, key ssh.PublicKey) bool {
		return ssh.KeysEqual(key, pubKey)
	})

	log.Fatal(ssh.ListenAndServe("127.0.0.1:2222", nil, publicKeyOption))
}

func TestSSHRepo(t *testing.T) {
	require := require.New(t)
	privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
	require.NoError(err)
	publicRsaKey, err := gossh.NewPublicKey(&privateKey.PublicKey)
	require.NoError(err)
	go startSshGitRepo(publicRsaKey, require)
	repotester := tasks.GitRepoTester{}

	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type:    "RSA PRIVATE KEY",
		Headers: nil,
		Bytes:   x509.MarshalPKCS1PrivateKey(privateKey),
	})

	privKey := b64.StdEncoding.EncodeToString(privatePEM)

	spec := api.RepositorySpec{}
	err = spec.FromGitRepoSpec(api.GitRepoSpec{
		Url:  "ssh://root@127.0.0.1:2222",
		Type: api.GitRepoSpecTypeGit,
		SshConfig: &api.SshConfig{
			SshPrivateKey:          &privKey,
			SkipServerVerification: lo.ToPtr(true),
		}})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("name")}, Spec: spec})

	require.NoError(err)
}

func TestOciRepoOpenRegistry(t *testing.T) {
	require := require.New(t)

	// Create a mock OCI registry that doesn't require auth
	mock := &MockOciRegistry{
		RequireAuth: false,
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()

	repotester := tasks.OciRepoTester{}

	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.NoError(err)
}

func TestOciRepoWithValidCredentials(t *testing.T) {
	require := require.New(t)

	// Create a mock OCI registry that requires auth
	mock := &MockOciRegistry{
		RequireAuth:   true,
		ValidUsername: "testuser",
		ValidPassword: "testpass",
		ServiceName:   "test-registry",
		AuthToken:     "authenticated-token-12345",
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	mock.AuthServerURL = server.URL

	repotester := tasks.OciRepoTester{}

	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
		OciAuth:  newOciAuth("testuser", "testpass"),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.NoError(err)
}

func TestOciRepoWithInvalidCredentials(t *testing.T) {
	require := require.New(t)

	// Create a mock OCI registry that requires auth
	mock := &MockOciRegistry{
		RequireAuth:   true,
		ValidUsername: "testuser",
		ValidPassword: "testpass",
		ServiceName:   "test-registry",
		AuthToken:     "authenticated-token-12345",
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	mock.AuthServerURL = server.URL

	repotester := tasks.OciRepoTester{}

	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
		OciAuth:  newOciAuth("wronguser", "wrongpass"),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.Error(err)
	require.Contains(err.Error(), "invalid credentials")
}

func TestOciRepoAnonymousAccess(t *testing.T) {
	require := require.New(t)

	// Create a mock OCI registry that allows anonymous token exchange with scope
	mock := &MockOciRegistry{
		RequireAuth:     true,
		ServiceName:     "test-registry",
		AnonymousToken:  "anonymous-token-12345",
		ReturnTokenName: "access_token", // Test access_token field
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	mock.AuthServerURL = server.URL

	repotester := tasks.OciRepoTester{}

	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.NoError(err)
}

func TestOciRepoMixedAuthRegistry(t *testing.T) {
	require := require.New(t)

	// Create a mock OCI registry that supports both authenticated and anonymous access
	mock := &MockOciRegistry{
		RequireAuth:    true,
		ValidUsername:  "testuser",
		ValidPassword:  "testpass",
		ServiceName:    "test-registry",
		AuthToken:      "authenticated-token-12345",
		AnonymousToken: "anonymous-token-67890",
	}
	server := httptest.NewServer(mock.Handler())
	defer server.Close()
	mock.AuthServerURL = server.URL

	repotester := tasks.OciRepoTester{}

	// Test with credentials - should get auth token
	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
		OciAuth:  newOciAuth("testuser", "testpass"),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.NoError(err)

	// Test without credentials - should get anonymous token via scope
	specAnon := api.RepositorySpec{}
	err = specAnon.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo-anon")}, Spec: specAnon})
	require.NoError(err)

	// Test with invalid credentials - should fail even though anonymous access is available
	specInvalid := api.RepositorySpec{}
	err = specInvalid.FromOciRepoSpec(api.OciRepoSpec{
		Registry: registryHost(server.URL),
		Type:     "oci",
		Scheme:   lo.ToPtr(api.Http),
		OciAuth:  newOciAuth("wronguser", "wrongpass"),
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo-invalid")}, Spec: specInvalid})
	require.Error(err)
	require.Contains(err.Error(), "invalid credentials")
}

func TestOciRepoInvalidRegistry(t *testing.T) {
	require := require.New(t)

	repotester := tasks.OciRepoTester{}

	spec := api.RepositorySpec{}
	err := spec.FromOciRepoSpec(api.OciRepoSpec{
		Registry: "localhost:99999", // Invalid port/unreachable
		Type:     "oci",
	})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
	require.Error(err)
	require.Contains(err.Error(), "failed to connect")
}
