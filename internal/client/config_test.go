package client

import (
	"context"
	"crypto/x509"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	configDir = "/etc/flightctl"
	certsDir  = "certs"
	certFile  = "some.crt"
	certData  = "certdata"
)

func TestValidConfig(t *testing.T) {
	require := require.New(t)

	testRootDir := t.TempDir()
	require.NoError(os.MkdirAll(filepath.Join(testRootDir, configDir, certsDir), 0700))
	require.NoError(os.WriteFile(filepath.Join(testRootDir, configDir, certsDir, certFile), []byte(certData), 0600))

	tests := []struct {
		name   string
		config Config
	}{
		{name: "only server", config: Config{Service: Service{Server: "https://localhost:3443"}}},
		{name: "server with CA cert data", config: Config{Service: Service{Server: "https://localhost:3443", CertificateAuthorityData: []byte(certData)}, testRootDir: testRootDir}},
		{name: "server with absolute path to CA file", config: Config{Service: Service{Server: "https://localhost:3443", CertificateAuthority: filepath.Join(configDir, certsDir, certFile)}, testRootDir: testRootDir}},
		{name: "server with relative path to CA file", config: Config{Service: Service{Server: "https://localhost:3443", CertificateAuthority: filepath.Join(certsDir, certFile)}, baseDir: configDir, testRootDir: testRootDir}},
		{name: "server with valid organization ID", config: Config{Service: Service{Server: "https://localhost:3443"}, Organization: "123e4567-e89b-12d3-a456-426614174000"}},
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
		config                 Config
		expectedErrorSubstring string
	}{
		{name: "no server", config: Config{}, expectedErrorSubstring: "no server found"},
		{name: "invalid server", config: Config{Service: Service{Server: "--"}}, expectedErrorSubstring: "invalid server"},
		{name: "conflicting ca", config: Config{Service: Service{Server: "https://localhost", CertificateAuthority: "ca", CertificateAuthorityData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "conflicting cert", config: Config{Service: Service{Server: "https://localhost"}, AuthInfo: AuthInfo{ClientCertificate: "cert", ClientCertificateData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "conflicting key", config: Config{Service: Service{Server: "https://localhost"}, AuthInfo: AuthInfo{ClientCertificate: "cert", ClientKey: "key", ClientKeyData: []byte{0}}}, expectedErrorSubstring: "both specified"},
		{name: "unreadable ca", config: Config{Service: Service{Server: "https://localhost", CertificateAuthority: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
		{name: "unreadable cert", config: Config{Service: Service{Server: "https://localhost"}, AuthInfo: AuthInfo{ClientCertificate: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
		{name: "unreadable key", config: Config{Service: Service{Server: "https://localhost"}, AuthInfo: AuthInfo{ClientCertificate: "cert", ClientKey: "does_not_exist"}}, expectedErrorSubstring: "unable to read"},
		{name: "invalid organization ID", config: Config{Service: Service{Server: "https://localhost"}, Organization: "not-a-uuid"}, expectedErrorSubstring: "invalid organization ID"},
		{name: "malformed organization UUID", config: Config{Service: Service{Server: "https://localhost"}, Organization: "12345678-1234-1234-1234-12345678901"}, expectedErrorSubstring: "invalid organization ID"},
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
	config.Service = Service{
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
			ctx := context.Background()

			testDirPath := t.TempDir()
			configFile := filepath.Join(testDirPath, "client.yaml")

			// generate the CA and client certs
			cfg := ca.NewDefault(testDirPath)
			ca, _, err := crypto.EnsureCA(cfg)
			require.NoError(err)
			clientCert, _, err := ca.EnsureClientCertificate(ctx, filepath.Join(testDirPath, "client-enrollment.crt"), filepath.Join(testDirPath, "client-enrollment.key"), cfg.ClientBootstrapCertName, cfg.ClientBootstrapValidityDays)
			require.NoError(err)

			// write client config to disk
			bundle, err := ca.GetCABundle()
			require.NoError(err)
			err = WriteConfig(configFile, tt.server, tt.serverName, bundle, clientCert)
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
			for _, caCert := range ca.GetCABundleX509() {
				caPool.AddCert(caCert)
			}
			require.True(caPool.Equal(httpTransport.TLSClientConfig.RootCAs))
		})
	}
}
