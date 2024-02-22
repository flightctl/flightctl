package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
	certutil "k8s.io/client-go/util/cert"
	"sigs.k8s.io/yaml"
)

// Config holds the information needed to connect to a FlightCtl API server
type Config struct {
	Service  Service  `json:"service"`
	AuthInfo AuthInfo `json:"authentication"`
}

// Service contains information how to connect to and authenticate the FlightCtl API server.
type Service struct {
	// Server is the URL of the FlightCtl API server (the part before /api/v1/...).
	Server string `json:"server"`
	// TLSServerName is passed to the server for SNI and is used in the client to check server certificates against.
	// If TLSServerName is empty, the hostname used to contact the server is used.
	// +optional
	TLSServerName string `json:"tls-server-name,omitempty"`
	// CertificateAuthorityData contains PEM-encoded certificate authority certificates.
	// +optional
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
}

// AuthInfo contains information for authenticating FlightCtl API clients.
type AuthInfo struct {
	// ClientCertificateData contains PEM-encoded data from a client cert file for TLS.
	// +optional
	ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
	// ClientKeyData contains PEM-encoded data from a client key file for TLS.
	// +optional
	ClientKeyData []byte `json:"client-key-data,omitempty" datapolicy:"security-key"`
}

// NewFromConfig returns a new FlightCtl API client from the given config.
func NewFromConfig(config *Config) (*client.ClientWithResponses, error) {
	caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
	if err != nil {
		return nil, fmt.Errorf("parsing CA certs: %v", err)
	}

	tlsServerName := config.Service.TLSServerName
	if len(tlsServerName) == 0 {
		u, err := url.Parse(config.Service.Server)
		if err != nil {
			return nil, fmt.Errorf("parsing CA certs: %v", err)
		}
		tlsServerName = u.Hostname()
	}

	clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
	if err != nil {
		return nil, fmt.Errorf("parsing client cert and key: %v", err)
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:      caPool,
				ServerName:   tlsServerName,
				Certificates: []tls.Certificate{clientCert},
				MinVersion:   tls.VersionTLS13,
			},
		},
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(httpClient), ref)
}

// NewFromConfigFile returns a new FlightCtl API client using the config read from the given file.
func NewFromConfigFile(filename string) (*client.ClientWithResponses, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config: %v", err)
	}
	var config Config
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}
	// TODO: validation
	return NewFromConfig(&config)
}

// WriteConfig writes a client config file using the given parameters.
func WriteConfig(filename string, server string, tlsServerName string, ca *crypto.TLSCertificateConfig, client *crypto.TLSCertificateConfig) error {
	caCertPEM, _, err := ca.GetPEMBytes()
	if err != nil {
		return fmt.Errorf("PEM-encoding CA certs: %v", err)
	}
	clientCertPEM, clientKeyPEM, err := client.GetPEMBytes()
	if err != nil {
		return fmt.Errorf("PEM-encoding client cert and key: %v", err)
	}

	config := Config{
		Service: Service{
			Server:                   server,
			TLSServerName:            tlsServerName,
			CertificateAuthorityData: caCertPEM,
		},
		AuthInfo: AuthInfo{
			ClientCertificateData: clientCertPEM,
			ClientKeyData:         clientKeyPEM,
		},
	}
	contents, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("encoding config: %v", err)
	}
	if err := os.WriteFile(filename, contents, 0600); err != nil {
		return fmt.Errorf("writing config: %v", err)
	}
	return nil
}
