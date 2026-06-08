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
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/gliderlabs/ssh"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
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

func startHttpsMTLSRepo(tlsConfig *tls.Config) {
	server := http.Server{
		Addr: "localhost:4443",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			path, err := filepath.Abs("./git/base")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			indexData, err := os.ReadFile(path)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = w.Write(indexData)
		}),
		TLSConfig: tlsConfig,
	}
	log.Fatal(server.ListenAndServeTLS("", ""))
}

func startSshGitRepo(ctx context.Context, pubKey gossh.PublicKey) <-chan error {
	errCh := make(chan error, 1)

	server := &ssh.Server{
		Addr: "127.0.0.1:2222",
		Handler: func(s ssh.Session) {
			path, err := filepath.Abs("./git/base")
			if err != nil {
				errCh <- fmt.Errorf("failed to get absolute path: %w", err)
				_ = s.Exit(1)
				return
			}
			indexData, err := os.ReadFile(path)
			if err != nil {
				errCh <- fmt.Errorf("failed to read test data file %s: %w", path, err)
				_ = s.Exit(1)
				return
			}
			_, _ = s.Write(indexData)
			<-s.Context().Done()
		},
		PublicKeyHandler: func(_ ssh.Context, key ssh.PublicKey) bool {
			return ssh.KeysEqual(key, pubKey)
		},
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != ssh.ErrServerClosed {
			errCh <- fmt.Errorf("SSH server error: %w", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()

	return errCh
}

func startTLSOciRegistryServer(mock *MockOciRegistry) (*httptest.Server, string) {
	ctx := context.Background()

	testDirPath := GinkgoT().TempDir()
	cfg := ca.NewDefault(testDirPath)
	testCA, _, err := crypto.EnsureCA(cfg)
	Expect(err).NotTo(HaveOccurred())

	serverCerts, _, err := testCA.EnsureServerCertificate(ctx,
		filepath.Join(testDirPath, "server.crt"),
		filepath.Join(testDirPath, "server.key"),
		[]string{"127.0.0.1"}, 1)
	Expect(err).NotTo(HaveOccurred())

	tlsConfig, _, err := crypto.TLSConfigForServer(testCA.GetCABundleX509(), serverCerts)
	Expect(err).NotTo(HaveOccurred())

	server := httptest.NewUnstartedServer(mock.Handler())
	server.TLS = tlsConfig
	server.StartTLS()

	caCertPEM, err := testCA.GetCABundle()
	Expect(err).NotTo(HaveOccurred())
	caCrtB64 := b64.StdEncoding.EncodeToString(caCertPEM)

	return server, caCrtB64
}

var _ = Describe("RepoTester", func() {
	Describe("HTTPS mTLS Repository", func() {
		It("should successfully test access with valid mTLS configuration", func() {
			ctx := context.Background()

			testDirPath := GinkgoT().TempDir()
			cfg := ca.NewDefault(testDirPath)
			testCA, _, err := crypto.EnsureCA(cfg)
			Expect(err).NotTo(HaveOccurred())

			serverCerts, _, err := testCA.EnsureServerCertificate(ctx, filepath.Join(testDirPath, "server.crt"), filepath.Join(testDirPath, "server.key"), []string{"localhost"}, 1)
			Expect(err).NotTo(HaveOccurred())

			adminCert, _, err := testCA.EnsureClientCertificate(ctx, filepath.Join(testDirPath, "client.crt"), filepath.Join(testDirPath, "client.key"), cfg.AdminCommonName, 1)
			Expect(err).NotTo(HaveOccurred())

			_, tlsConfig, err := crypto.TLSConfigForServer(testCA.GetCABundleX509(), serverCerts)
			Expect(err).NotTo(HaveOccurred())

			go startHttpsMTLSRepo(tlsConfig)
			repotester := tasks.GitRepoTester{}

			clientCertPEM, clientKeyPEM, err := adminCert.GetPEMBytes()
			Expect(err).NotTo(HaveOccurred())
			caCertPEM, err := testCA.GetCABundle()
			Expect(err).NotTo(HaveOccurred())

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
			Expect(err).NotTo(HaveOccurred())

			err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("name")}, Spec: spec})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Describe("SSH Repository", func() {
		It("should successfully test access with valid SSH key", func() {
			privateKey, err := rsa.GenerateKey(rand.Reader, 4096)
			Expect(err).NotTo(HaveOccurred())
			publicRsaKey, err := gossh.NewPublicKey(&privateKey.PublicKey)
			Expect(err).NotTo(HaveOccurred())

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sshErrCh := startSshGitRepo(ctx, publicRsaKey)

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
			Expect(err).NotTo(HaveOccurred())

			err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("name")}, Spec: spec})
			Expect(err).NotTo(HaveOccurred())

			select {
			case sshErr := <-sshErrCh:
				Expect(sshErr).NotTo(HaveOccurred())
			default:
			}
		})
	})

	Describe("OCI Repository", func() {
		Context("with open registry (no auth required)", func() {
			It("should successfully test access", func() {
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
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with valid credentials", func() {
			It("should successfully test access", func() {
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
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with invalid credentials", func() {
			It("should fail with invalid credentials error", func() {
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
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid credentials"))
			})
		})

		Context("with anonymous access", func() {
			It("should successfully test access with anonymous token", func() {
				mock := &MockOciRegistry{
					RequireAuth:     true,
					ServiceName:     "test-registry",
					AnonymousToken:  "anonymous-token-12345",
					ReturnTokenName: "access_token",
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
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with mixed auth registry", func() {
			It("should handle both authenticated and anonymous access correctly", func() {
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
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())

				// Test without credentials - should get anonymous token via scope
				specAnon := api.RepositorySpec{}
				err = specAnon.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHost(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo-anon")}, Spec: specAnon})
				Expect(err).NotTo(HaveOccurred())

				// Test with invalid credentials - should fail even though anonymous access is available
				specInvalid := api.RepositorySpec{}
				err = specInvalid.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHost(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Http),
					OciAuth:  newOciAuth("wronguser", "wrongpass"),
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo-invalid")}, Spec: specInvalid})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid credentials"))
			})
		})

		Context("with invalid registry", func() {
			It("should fail with connection error", func() {
				repotester := tasks.OciRepoTester{}

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: "localhost:99999",
					Type:     "oci",
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-repo")}, Spec: spec})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to connect"))
			})
		})

		Context("with TLS and CA certificate", func() {
			It("should successfully test access", func() {
				mock := &MockOciRegistry{RequireAuth: false}
				server, caCrtB64 := startTLSOciRegistryServer(mock)
				defer server.Close()

				repotester := tasks.OciRepoTester{}

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHost(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Https),
					CaCrt:    &caCrtB64,
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-tls")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with TLS, CA certificate and auth", func() {
			It("should successfully test access", func() {
				mock := &MockOciRegistry{
					RequireAuth:   true,
					ValidUsername: "testuser",
					ValidPassword: "testpass",
					ServiceName:   "test-registry",
					AuthToken:     "authenticated-token-12345",
				}
				server, caCrtB64 := startTLSOciRegistryServer(mock)
				defer server.Close()
				mock.AuthServerURL = server.URL

				repotester := tasks.OciRepoTester{}

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHost(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Https),
					CaCrt:    &caCrtB64,
					OciAuth:  newOciAuth("testuser", "testpass"),
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-tls-auth")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with skip server verification", func() {
			It("should successfully test access without CA cert", func() {
				mock := &MockOciRegistry{RequireAuth: false}
				server := httptest.NewTLSServer(mock.Handler())
				defer server.Close()

				repotester := tasks.OciRepoTester{}

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry:               registryHost(server.URL),
					Type:                   "oci",
					Scheme:                 lo.ToPtr(api.Https),
					SkipServerVerification: lo.ToPtr(true),
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-insecure")}, Spec: spec})
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Context("with TLS but without CA cert", func() {
			It("should fail with connection error", func() {
				mock := &MockOciRegistry{RequireAuth: false}
				server, _ := startTLSOciRegistryServer(mock)
				defer server.Close()

				repotester := tasks.OciRepoTester{}

				spec := api.RepositorySpec{}
				err := spec.FromOciRepoSpec(api.OciRepoSpec{
					Registry: registryHost(server.URL),
					Type:     "oci",
					Scheme:   lo.ToPtr(api.Https),
				})
				Expect(err).NotTo(HaveOccurred())

				err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr("test-oci-no-ca")}, Spec: spec})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to connect"))
			})
		})
	})
})
