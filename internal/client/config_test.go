package client

import (
	"crypto/x509"
	"net/http"
	"path/filepath"
	"testing"

	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/stretchr/testify/require"
)

const (
	caCertValidityDays          = 365 * 10
	serverCertValidityDays      = 365 * 1
	clientBootStrapValidityDays = 365 * 1
	signerCertName              = "ca"
	clientBootstrapCertName     = "client-enrollment"
)

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
			server:         "https://localhost:3333",
			serverName:     "",
			serverWant:     "https://localhost:3333/",
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
