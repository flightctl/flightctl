package client

import (
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	configDir = "/etc/flightctl"
	certsDir  = "certs"
	certFile  = "some.crt"
	certData  = "certdata"

	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	signerCertName              = "ca"
	clientBootstrapCertName     = "client-enrollment"
)

func TestValidConfig(t *testing.T) {
	require := require.New(t)

	testRootDir := t.TempDir()
	require.NoError(os.MkdirAll(filepath.Join(testRootDir, configDir, certsDir), 0700))
	require.NoError(os.WriteFile(filepath.Join(testRootDir, configDir, certsDir, certFile), []byte(certData), 0600))

	tests := []struct {
		name   string
		config types.Config
	}{
		{name: "only server", config: types.Config{Service: types.Service{Server: "https://localhost:3443"}}},
		{name: "server with CA cert data", config: types.Config{Service: types.Service{Server: "https://localhost:3443", CertificateAuthorityData: []byte(certData)}, TestRootDir: testRootDir}},
		{name: "server with absolute path to CA file", config: types.Config{Service: types.Service{Server: "https://localhost:3443", CertificateAuthority: filepath.Join(configDir, certsDir, certFile)}, TestRootDir: testRootDir}},
		{name: "server with relative path to CA file", config: types.Config{Service: types.Service{Server: "https://localhost:3443", CertificateAuthority: filepath.Join(certsDir, certFile)}, BaseDir: configDir, TestRootDir: testRootDir}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NoError(t, tt.config.Validate())
		})
	}
}

func TestInvalidConfig(t *testing.T) {
	assert := assert.New(t)
	tests := []struct {
		name                   string
		config                 types.Config
		expectedErrorSubstring string
	}{
		{name: "no server", config: types.Config{}, expectedErrorSubstring: "no server found"},
		{name: "invalid server", config: types.Config{Service: types.Service{Server: "--"}}, expectedErrorSubstring: "invalid server"},
		{name: "conflicting ca", config: types.Config{Service: types.Service{Server: "https://localhost", CertificateAuthority: "ca", CertificateAuthorityData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "conflicting cert", config: types.Config{Service: types.Service{Server: "https://localhost"}, AuthInfo: types.AuthInfo{ClientCertificate: "cert", ClientCertificateData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "conflicting key", config: types.Config{Service: types.Service{Server: "https://localhost"}, AuthInfo: types.AuthInfo{ClientCertificate: "cert", ClientKey: "key", ClientKeyData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "unreadable ca", config: types.Config{Service: types.Service{Server: "https://localhost", CertificateAuthority: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
		{name: "unreadable cert", config: types.Config{Service: types.Service{Server: "https://localhost"}, AuthInfo: types.AuthInfo{ClientCertificate: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
		{name: "unreadable key", config: types.Config{Service: types.Service{Server: "https://localhost"}, AuthInfo: types.AuthInfo{ClientCertificate: "cert", ClientKey: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorContains(tt.config.Validate(), tt.expectedErrorSubstring)
		})
	}
}

func TestFlattenedConfig(t *testing.T) {
	require := require.New(t)

	testRootDir := t.TempDir()
	os.Setenv(TestRootDirEnvKey, testRootDir)
	require.NoError(os.MkdirAll(filepath.Join(testRootDir, configDir, certsDir), 0700))
	require.NoError(os.WriteFile(filepath.Join(testRootDir, configDir, certsDir, certFile), []byte(certData), 0600))

	config := NewDefault()
	config.Service = types.Service{
		Server:               "https://localhost",
		CertificateAuthority: filepath.Join(configDir, certsDir, certFile),
	}
	require.NoError(config.Flatten())
	require.Equal(config.Service.CertificateAuthorityData, []byte(certData))
}

func TestClientConfig(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name           string
		server         string
		serverWant     string
		serverName     string
		serverNameWant string
	}{
		{
			name:           "local service",
			server:         "https://localhost:3443",
			serverName:     "",
			serverWant:     "https://localhost:3443/",
			serverNameWant: "localhost",
		},
		{
			name:           "remote service",
			server:         "https://api.flightctl.edge-devices.net/devicemanagement/",
			serverName:     "flightctl.edge-devices.net",
			serverWant:     "https://api.flightctl.edge-devices.net/devicemanagement/",
			serverNameWant: "flightctl.edge-devices.net",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDirPath := t.TempDir()
			configFile := filepath.Join(testDirPath, "client.yaml")

			// generate the CA and client certs
			ca, _, err := crypto.EnsureCA(filepath.Join(testDirPath, "ca.crt"), filepath.Join(testDirPath, "ca.key"), "", signerCertName, caCertValidityDays)
			require.NoError(err)
			clientCert, _, err := ca.EnsureClientCertificate(filepath.Join(testDirPath, "client-enrollment.crt"), filepath.Join(testDirPath, "client-enrollment.key"), clientBootstrapCertName, clientBootStrapValidityDays)
			require.NoError(err)

			// write client config to disk
			err = WriteConfig(configFile, tt.server, tt.serverName, ca.Config, clientCert)
			require.NoError(err)

			// read client config from disk and create API client from it
			client, err := NewFromConfigFile(configFile)
			require.NoError(err)
			require.NotNil(client)

			// test results match expected
			c, ok := client.ClientInterface.(*apiclient.Client)
			require.True(ok)
			require.Equal(tt.serverWant, c.Server)

			httpClient, ok := c.Client.(*http.Client)
			require.True(ok)

			httpTransport, ok := httpClient.Transport.(*http.Transport)
			require.True(ok)
			require.Equal(tt.serverNameWant, httpTransport.TLSClientConfig.ServerName)
			require.NotEmpty(httpTransport.TLSClientConfig.Certificates)
			require.ElementsMatch(clientCert.Certs[0].Raw, httpTransport.TLSClientConfig.Certificates[0].Certificate[0])
			require.NotNil(httpTransport.TLSClientConfig.RootCAs)
			caPool := x509.NewCertPool()
			for _, caCert := range ca.Config.Certs {
				caPool.AddCert(caCert)
			}
			require.True(caPool.Equal(httpTransport.TLSClientConfig.RootCAs))
		})
	}
}
