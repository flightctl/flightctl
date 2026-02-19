package tasks

import (
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
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func testLogger() logrus.FieldLogger {
	l := logrus.New()
	l.SetLevel(logrus.DebugLevel)
	return l
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

func TestNewOCIAuthClient_Default(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
	}

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Nil(t, client.Client, "default transport should not be overridden when no TLS options set")
	require.Nil(t, client.Credential, "no credentials should be set")
}

func TestNewOCIAuthClient_SkipVerification(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(true),
	}

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.Nil(t, transport.TLSClientConfig.RootCAs)
}

func TestNewOCIAuthClient_CaCrt(t *testing.T) {
	caPEM := generateTestCACertPEM(t)
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		CaCrt:    &encoded,
	}

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.False(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.NotNil(t, transport.TLSClientConfig.RootCAs)
	require.Equal(t, tls.VersionTLS12, int(transport.TLSClientConfig.MinVersion))
}

func TestNewOCIAuthClient_BothCaCrtAndSkipVerification(t *testing.T) {
	caPEM := generateTestCACertPEM(t)
	encoded := base64.StdEncoding.EncodeToString([]byte(caPEM))

	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(true),
		CaCrt:                  &encoded,
	}

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Client)

	transport, ok := client.Client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport.TLSClientConfig)
	require.True(t, transport.TLSClientConfig.InsecureSkipVerify)
	require.NotNil(t, transport.TLSClientConfig.RootCAs)
}

func TestNewOCIAuthClient_WithCredentials(t *testing.T) {
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

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.Credential, "credentials should be set")
}

func TestNewOCIAuthClient_InvalidBase64CaCrt(t *testing.T) {
	invalid := "not-valid-base64!!!"
	ociSpec := &coredomain.OciRepoSpec{
		Registry: "registry.example.com",
		Type:     coredomain.OciRepoSpecTypeOci,
		CaCrt:    &invalid,
	}

	_, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to decode CA certificate")
}

func TestNewOCIAuthClient_SkipVerificationFalse(t *testing.T) {
	ociSpec := &coredomain.OciRepoSpec{
		Registry:               "registry.example.com",
		Type:                   coredomain.OciRepoSpecTypeOci,
		SkipServerVerification: lo.ToPtr(false),
	}

	client, err := newOCIAuthClient(ociSpec, "registry.example.com", testLogger())
	require.NoError(t, err)
	require.NotNil(t, client)
	require.Nil(t, client.Client, "default transport should not be overridden when SkipServerVerification is false")
}
