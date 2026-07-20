package oci

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
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/pkg/crypto"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"oras.land/oras-go/v2/registry/remote/auth"
)

func TestMain(m *testing.M) {
	log := logrus.StandardLogger()
	dir, err := os.MkdirTemp("", "oci-test-encryption-*")
	if err != nil {
		log.Fatalf("creating temp encryption dir: %v", err)
	}
	defer os.RemoveAll(dir)

	keyPath := filepath.Join(dir, "key")
	key, err := crypto.GenerateAES256Key()
	if err != nil {
		log.Fatalf("generating test encryption key: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte(key), 0600); err != nil {
		log.Fatalf("writing test encryption key: %v", err)
	}

	cfg := config.NewDefault()
	cfg.Encryption = &config.EncryptionConfig{
		Keys:        []config.EncryptionKeyConfig{{ID: "default", Path: keyPath}},
		ActiveKeyID: "default",
	}
	if err := encryption.InitGlobalEncryption(log, cfg); err != nil {
		log.Fatalf("initializing test encryption: %v", err)
	}

	os.Exit(m.Run())
}

func generateTestCACertPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{Organization: []string{"Test CA"}},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes}))
}

// authClientFrom extracts the *auth.Client from a BuildOciRepoRef result.
func authClientFrom(t *testing.T, ociSpec *coredomain.OciRepoSpec) *auth.Client {
	t.Helper()
	repoRef, err := BuildOciRepoRef(context.Background(), ociSpec, ociSpec.Registry+"/test-image")
	require.NoError(t, err)
	client, ok := repoRef.Client.(*auth.Client)
	require.True(t, ok, "repoRef.Client must be *auth.Client")
	return client
}

func TestBuildOciRepoRef_Default(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
	}

	client := authClientFrom(t, ociSpec)
	require.Nil(t, client.Client, "default transport should not be overridden when no TLS options set")
	require.Nil(t, client.Credential, "no credentials should be set")
}

func TestBuildOciRepoRef_SkipVerification(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(true),
	}

	client := authClientFrom(t, ociSpec)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.Nil(t, transport.TLSClientConfig.RootCAs)
}

func TestBuildOciRepoRef_CaCrt(t *testing.T) {
	caPEM := generateTestCACertPEM(t)
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		CaCrt:    &encoded,
	}

	client := authClientFrom(t, ociSpec)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.NotNil(t, transport.TLSClientConfig.RootCAs)
	require.Equal(t, tls.VersionTLS12, int(transport.TLSClientConfig.MinVersion))
}

func TestBuildOciRepoRef_BothCaCrtAndSkipVerification(t *testing.T) {
	caPEM := generateTestCACertPEM(t)
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(true),
		CaCrt:                  &encoded,
	}

	client := authClientFrom(t, ociSpec)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.NotNil(t, transport.TLSClientConfig.RootCAs)
}

func TestBuildOciRepoRef_WithCredentials(t *testing.T) {
	ociAuth := &v1beta1.OciAuth{}
	err := ociAuth.FromDockerAuth(v1beta1.DockerAuth{
		AuthType: v1beta1.Docker,
		Username: "testuser",
		Password: "testpass",
	})
	require.NoError(t, err)

	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		OciAuth:  ociAuth,
	}

	client := authClientFrom(t, ociSpec)
	require.NotNil(t, client.Credential, "credentials should be set")
}

func TestBuildOciRepoRef_InvalidBase64CaCrt(t *testing.T) {
	invalid := "not-valid-base64!!!"
	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		CaCrt:    &invalid,
	}

	_, err := BuildOciRepoRef(context.Background(), ociSpec, ociSpec.Registry+"/test-image")
	require.Error(t, err)
	require.Contains(t, err.Error(), "decode CA certificate")
}

func TestBuildOciRepoRef_SkipVerificationFalse(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(false),
	}

	client := authClientFrom(t, ociSpec)
	require.Nil(t, client.Client, "default transport should not be overridden when SkipServerVerification is false")
}

func TestBuildOciRepoRef_PlainHTTP(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		Scheme:   lo.ToPtr(coredomain.OciRepoSchemeHttp),
	}

	repoRef, err := BuildOciRepoRef(context.Background(), ociSpec, ociSpec.Registry+"/test-image")
	require.NoError(t, err)
	require.True(t, repoRef.PlainHTTP)
}
