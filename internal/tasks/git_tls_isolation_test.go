package tasks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type gitHTTPSFixture struct {
	server        *httptest.Server
	caPEM         []byte
	clientCertPEM []byte
	clientKeyPEM  []byte
	mu            sync.Mutex
	clients       []string
}

func newGitHTTPSFixture(t *testing.T, clientName string) *gitHTTPSFixture {
	t.Helper()
	// An empty Git repository completes TLS negotiation without requiring a packfile.
	return newGitHTTPSFixtureWithAdvertisement(t, clientName, "001e# service=git-upload-pack\n00000000")
}

func newGitHTTPSFixtureWithAdvertisement(t *testing.T, clientName, advertisement string) *gitHTTPSFixture {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-git-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	ca, err = x509.ParseCertificate(caDER)
	require.NoError(t, err)
	serverCert, _, _ := gitTLSLeafCertificate(t, ca, caKey, 2, "test-git-server", x509.ExtKeyUsageServerAuth)
	_, clientCert, clientKey := gitTLSLeafCertificate(t, ca, caKey, 3, clientName, x509.ExtKeyUsageClientAuth)
	fixture := &gitHTTPSFixture{
		caPEM:         pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		clientCertPEM: clientCert,
		clientKeyPEM:  clientKey,
	}
	fixture.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		client := ""
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			client = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		fixture.mu.Lock()
		fixture.clients = append(fixture.clients, client)
		fixture.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		if _, err := io.WriteString(w, advertisement); err != nil {
			t.Errorf("write Git advertisement: %v", err)
		}
	}))
	fixture.server.TLS = &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequestClientCert,
	}
	fixture.server.Config.ErrorLog = log.New(io.Discard, "", 0)
	fixture.server.StartTLS()
	t.Cleanup(fixture.server.Close)
	return fixture
}

func gitTLSLeafCertificate(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, serial int64, name string, usage x509.ExtKeyUsage) (tls.Certificate, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	return pair, certPEM, keyPEM
}

func (f *gitHTTPSFixture) repository(t *testing.T, httpConfig *domain.HttpConfig) *domain.Repository {
	t.Helper()
	spec := domain.RepositorySpec{}
	require.NoError(t, spec.FromGitRepoSpec(domain.GitRepoSpec{
		Type:       domain.GitRepoSpecTypeGit,
		Url:        f.server.URL + "/repo.git",
		HttpConfig: httpConfig,
	}))
	repo := &domain.Repository{Spec: spec}
	repo.Metadata.Name = lo.ToPtr("tls-repository")
	return repo
}

func (f *gitHTTPSFixture) mutualTLSConfig() *domain.HttpConfig {
	return &domain.HttpConfig{
		CaCrt:  lo.ToPtr(base64.StdEncoding.EncodeToString(f.caPEM)),
		TlsCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(f.clientCertPEM)),
		TlsKey: lo.ToPtr(base64.StdEncoding.EncodeToString(f.clientKeyPEM)),
	}
}

func (f *gitHTTPSFixture) clientNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.clients...)
}

type gitHTTPSOperation struct {
	name string
	run  func(*domain.Repository) error
}

func gitHTTPSOperations() []gitHTTPSOperation {
	return []gitHTTPSOperation{
		{name: "clone", run: func(repo *domain.Repository) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _, err := CloneGitRepo(ctx, repo, nil, nil, nil)
			return err
		}},
		{name: "repository connectivity", run: func(repo *domain.Repository) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return (&GitRepoTester{}).TestAccess(ctx, repo)
		}},
	}
}

func TestGitHTTPSIsolation(t *testing.T) {
	testGitHTTPSIsolation(t, gitHTTPSOperations())
}

func testGitHTTPSIsolation(t *testing.T, operations []gitHTTPSOperation) {
	t.Helper()
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			fixture := newGitHTTPSFixture(t, "client-a")
			cases := []struct {
				name       string
				first      *domain.HttpConfig
				second     *domain.HttpConfig
				firstCert  string
				secondOK   bool
				secondCert string
			}{
				{name: "When verification is skipped it should stay enabled for a repository without HTTP config", first: &domain.HttpConfig{SkipServerVerification: lo.ToPtr(true)}},
				{name: "When a custom CA is trusted it should not be trusted by a repository without HTTP config", first: &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.caPEM))}},
				{name: "When a client certificate is used it should not reach a repository without HTTP config", first: fixture.mutualTLSConfig(), firstCert: "client-a"},
				{name: "When verification is explicitly enabled it should reject an untrusted server", first: &domain.HttpConfig{SkipServerVerification: lo.ToPtr(true)}, second: &domain.HttpConfig{SkipServerVerification: lo.ToPtr(false)}},
				{name: "When HTTP config is empty it should reject an untrusted server", first: fixture.mutualTLSConfig(), second: &domain.HttpConfig{}, firstCert: "client-a"},
				{name: "When a repository has no client certificate it should send none", first: fixture.mutualTLSConfig(), second: &domain.HttpConfig{CaCrt: lo.ToPtr(base64.StdEncoding.EncodeToString(fixture.caPEM))}, firstCert: "client-a", secondOK: true},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					before := len(fixture.clientNames())
					err := operation.run(fixture.repository(t, tc.first))
					require.Error(t, err)
					require.Contains(t, err.Error(), transport.ErrEmptyRemoteRepository.Error())
					require.Equal(t, []string{tc.firstCert}, fixture.clientNames()[before:])
					err = operation.run(fixture.repository(t, tc.second))
					require.Error(t, err)
					if tc.secondOK {
						require.Contains(t, err.Error(), transport.ErrEmptyRemoteRepository.Error())
						require.Equal(t, []string{tc.firstCert, tc.secondCert}, fixture.clientNames()[before:])
					} else {
						require.Contains(t, err.Error(), "certificate")
						require.Equal(t, []string{tc.firstCert}, fixture.clientNames()[before:])
					}
				})
			}
		})
	}
}

func TestGitHTTPSIsolationConcurrent(t *testing.T) {
	testGitHTTPSIsolationConcurrent(t, gitHTTPSOperations())
}

func testGitHTTPSIsolationConcurrent(t *testing.T, operations []gitHTTPSOperation) {
	t.Helper()
	for _, operation := range operations {
		t.Run("When "+operation.name+" operations overlap it should isolate CA trust and client identities", func(t *testing.T) {
			first := newGitHTTPSFixture(t, "client-a")
			second := newGitHTTPSFixture(t, "client-b")
			repositories := []*domain.Repository{first.repository(t, first.mutualTLSConfig()), second.repository(t, second.mutualTLSConfig())}
			var calls []gitHTTPSConcurrentOperation
			for _, repo := range repositories {
				calls = append(calls, gitHTTPSConcurrentOperation{operation: operation, repository: repo, errorContains: transport.ErrEmptyRemoteRepository.Error()})
			}
			runGitHTTPSOperationsConcurrently(t, calls)
			require.Len(t, first.clientNames(), gitHTTPSConcurrentAttempts)
			require.Len(t, second.clientNames(), gitHTTPSConcurrentAttempts)
			for _, client := range first.clientNames() {
				require.Equal(t, "client-a", client)
			}
			for _, client := range second.clientNames() {
				require.Equal(t, "client-b", client)
			}
		})
	}
}

const gitHTTPSConcurrentAttempts = 12

type gitHTTPSConcurrentOperation struct {
	operation     gitHTTPSOperation
	repository    *domain.Repository
	errorContains string
}

func runGitHTTPSOperationsConcurrently(t *testing.T, calls []gitHTTPSConcurrentOperation) {
	t.Helper()
	failures := make(chan error, gitHTTPSConcurrentAttempts*len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range gitHTTPSConcurrentAttempts {
		for _, call := range calls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if err := call.operation.run(call.repository); err == nil || !strings.Contains(err.Error(), call.errorContains) {
					failures <- fmt.Errorf("%s: unexpected Git response: %v", call.operation.name, err)
				}
			}()
		}
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
}

func TestGitHTTPSVerificationIsolationConcurrent(t *testing.T) {
	operations := append(gitHTTPSOperations(), gitHTTPSListOperations()...)
	for _, operation := range operations {
		t.Run("When "+operation.name+" operations overlap it should isolate insecure verification", func(t *testing.T) {
			fixture := newGitHTTPSFixture(t, "client-unused")
			insecureRepo := fixture.repository(t, &domain.HttpConfig{SkipServerVerification: lo.ToPtr(true)})
			defaultRepo := fixture.repository(t, nil)
			runGitHTTPSOperationsConcurrently(t, []gitHTTPSConcurrentOperation{
				{operation: operation, repository: insecureRepo, errorContains: transport.ErrEmptyRemoteRepository.Error()},
				{operation: operation, repository: defaultRepo, errorContains: "certificate"},
			})
			clients := fixture.clientNames()
			require.Len(t, clients, gitHTTPSConcurrentAttempts, "only the explicitly insecure repository should reach the server")
			for _, client := range clients {
				require.Empty(t, client)
			}
		})
	}
}

func TestGitHTTPSMixedOperationsConcurrent(t *testing.T) {
	t.Run("When clone and dependency probes overlap it should isolate CA trust and client identities", func(t *testing.T) {
		first := newGitHTTPSFixture(t, "client-clone")
		second := newGitHTTPSFixture(t, "client-probe")
		clone := gitHTTPSOperations()[0]
		probe := gitHTTPSListOperations()[1]
		runGitHTTPSOperationsConcurrently(t, []gitHTTPSConcurrentOperation{
			{operation: clone, repository: first.repository(t, first.mutualTLSConfig()), errorContains: transport.ErrEmptyRemoteRepository.Error()},
			{operation: probe, repository: second.repository(t, second.mutualTLSConfig()), errorContains: transport.ErrEmptyRemoteRepository.Error()},
		})
		for _, fixture := range []*gitHTTPSFixture{first, second} {
			require.Len(t, fixture.clientNames(), gitHTTPSConcurrentAttempts)
		}
		for _, client := range first.clientNames() {
			require.Equal(t, "client-clone", client)
		}
		for _, client := range second.clientNames() {
			require.Equal(t, "client-probe", client)
		}
	})
}
