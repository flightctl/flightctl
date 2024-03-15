package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
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
	// CertificateAuthority is the path to a cert file for the certificate authority.
	CertificateAuthority string `json:"certificate-authority,omitempty"`
	// CertificateAuthorityData contains PEM-encoded certificate authority certificates. Overrides CertificateAuthority
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`
}

// AuthInfo contains information for authenticating FlightCtl API clients.
type AuthInfo struct {
	// ClientCertificate is the path to a client cert file for TLS.
	// +optional
	ClientCertificate string `json:"client-certificate,omitempty"`
	// ClientCertificateData contains PEM-encoded data from a client cert file for TLS. Overrides ClientCertificate.
	// +optional
	ClientCertificateData []byte `json:"client-certificate-data,omitempty"`
	// ClientKey is the path to a client key file for TLS.
	// +optional
	ClientKey string `json:"client-key,omitempty"`
	// ClientKeyData contains PEM-encoded data from a client key file for TLS. Overrides ClientKey.
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
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := config.Flatten(); err != nil {
		return nil, err
	}
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

func (c *Config) Validate() error {
	validationErrors := make([]error, 0)
	validationErrors = append(validationErrors, validateService(c.Service)...)
	validationErrors = append(validationErrors, validateAuthInfo(c.AuthInfo)...)
	if len(validationErrors) > 0 {
		return fmt.Errorf("invalid configuration: %v", utilerrors.NewAggregate(validationErrors).Error())
	}
	return nil
}

func validateService(service Service) []error {
	validationErrors := make([]error, 0)
	// Make sure the server is specified and well-formed
	if len(service.Server) == 0 {
		validationErrors = append(validationErrors, fmt.Errorf("no server found"))
	} else {
		u, err := url.Parse(service.Server)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: %w", service.Server, err))
		}
		if len(u.Hostname()) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("invalid server format %q: no hostname", service.Server))
		}
	}
	// Make sure CA data and CA file aren't both specified
	if len(service.CertificateAuthority) != 0 && len(service.CertificateAuthorityData) != 0 {
		validationErrors = append(validationErrors, fmt.Errorf("certificate-authority-data and certificate-authority are both specified. certificate-authority-data will override"))
	}
	if len(service.CertificateAuthority) != 0 {
		clientCertCA, err := os.Open(service.CertificateAuthority)
		if err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("unable to read certificate-authority %v due to %w", service.CertificateAuthority, err))
		} else {
			defer clientCertCA.Close()
		}
	}
	return validationErrors
}

func validateAuthInfo(authInfo AuthInfo) []error {
	validationErrors := make([]error, 0)
	if len(authInfo.ClientCertificate) != 0 || len(authInfo.ClientCertificateData) != 0 {
		// Make sure cert data and file aren't both specified
		if len(authInfo.ClientCertificate) != 0 && len(authInfo.ClientCertificateData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-cert-data and client-cert are both specified. client-cert-data will override"))
		}
		// Make sure key data and file aren't both specified
		if len(authInfo.ClientKey) != 0 && len(authInfo.ClientKeyData) != 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data and client-key are both specified; client-key-data will override"))
		}
		// Make sure a key is specified
		if len(authInfo.ClientKey) == 0 && len(authInfo.ClientKeyData) == 0 {
			validationErrors = append(validationErrors, fmt.Errorf("client-key-data or client-key must be specified to use the clientCert authentication method"))
		}

		if len(authInfo.ClientCertificate) != 0 {
			clientCertFile, err := os.Open(authInfo.ClientCertificate)
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-cert %v due to %w", authInfo.ClientCertificate, err))
			} else {
				defer clientCertFile.Close()
			}
		}
		if len(authInfo.ClientKey) != 0 {
			clientKeyFile, err := os.Open(authInfo.ClientKey)
			if err != nil {
				validationErrors = append(validationErrors, fmt.Errorf("unable to read client-key %v due to %w", authInfo.ClientKey, err))
			} else {
				defer clientKeyFile.Close()
			}
		}
	}
	return validationErrors
}

// Reads the contents of all referenced files and embeds them in the config.
func (c *Config) Flatten() error {
	if err := flatten(&c.Service.CertificateAuthority, &c.Service.CertificateAuthorityData, ""); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientCertificate, &c.AuthInfo.ClientCertificateData, ""); err != nil {
		return err
	}
	if err := flatten(&c.AuthInfo.ClientKey, &c.AuthInfo.ClientKeyData, ""); err != nil {
		return err
	}
	return nil
}

func flatten(path *string, contents *[]byte, baseDir string) error {
	if len(*path) != 0 {
		if len(*contents) > 0 {
			return errors.New("cannot have values for both path and contents")
		}

		var err error
		absPath := resolvePath(*path, baseDir)
		*contents, err = os.ReadFile(absPath)
		if err != nil {
			return err
		}

		*path = ""
	}
	return nil
}

func resolvePath(path string, base string) string {
	// Don't resolve empty paths
	if len(path) > 0 {
		// Don't resolve absolute paths
		if !filepath.IsAbs(path) {
			return filepath.Join(base, path)
		}
	}
	return path
}
