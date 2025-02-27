//nolint:gosec
package tasks_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	b64 "encoding/base64"
	"encoding/pem"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/coreos/ignition/v2/config/util"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/gliderlabs/ssh"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
)

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
	require := require.New(t)

	testDirPath := t.TempDir()
	ca, _, err := crypto.EnsureCA(filepath.Join(testDirPath, "ca.crt"), filepath.Join(testDirPath, "ca.key"), "", "ca", 1)
	require.NoError(err)

	serverCerts, _, err := ca.EnsureServerCertificate(filepath.Join(testDirPath, "server.crt"), filepath.Join(testDirPath, "server.key"), []string{"localhost"}, 1)
	require.NoError(err)

	adminCert, _, err := ca.EnsureClientCertificate(filepath.Join(testDirPath, "client.crt"), filepath.Join(testDirPath, "client.key"), crypto.AdminCommonName, 1)
	require.NoError(err)

	_, tlsConfig, err := crypto.TLSConfigForServer(ca.Config, serverCerts)
	require.NoError(err)

	go startHttpsMTLSRepo(tlsConfig, require)
	repotester := tasks.GitRepoTester{}

	clientCertPEM, clientKeyPEM, err := adminCert.GetPEMBytes()
	require.NoError(err)
	caCertPEM, _, err := ca.Config.GetPEMBytes()
	require.NoError(err)

	clientCrtB64 := b64.StdEncoding.EncodeToString(clientCertPEM)
	clientKeyB64 := b64.StdEncoding.EncodeToString(clientKeyPEM)
	caB64 := b64.StdEncoding.EncodeToString(caCertPEM)

	spec := api.RepositorySpec{}
	err = spec.FromHttpRepoSpec(api.HttpRepoSpec{
		Url: "https://localhost:4443",
		HttpConfig: api.HttpConfig{
			TlsKey: &clientKeyB64,
			TlsCrt: &clientCrtB64,
			CaCrt:  &caB64,
		}})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: util.StrToPtr("name")}, Spec: spec})

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
	err = spec.FromSshRepoSpec(api.SshRepoSpec{
		Url: "ssh://root@127.0.0.1:2222",
		SshConfig: api.SshConfig{
			SshPrivateKey:          &privKey,
			SkipServerVerification: lo.ToPtr(true),
		}})
	require.NoError(err)

	err = repotester.TestAccess(&api.Repository{Metadata: api.ObjectMeta{Name: util.StrToPtr("name")}, Spec: spec})

	require.NoError(err)
}
