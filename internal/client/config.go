package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/types"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/middleware"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	certutil "k8s.io/client-go/util/cert"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/yaml"
)

const (
	// TestRootDirEnvKey is the environment variable key used to set the file system root when testing.
	TestRootDirEnvKey = "FLIGHTCTL_TEST_ROOT_DIR"
)

func NewDefault() *types.Config {
	c := &types.Config{}

	if value := os.Getenv(TestRootDirEnvKey); value != "" {
		c.TestRootDir = filepath.Clean(value)
	}

	return c
}

// NewFromConfig returns a new Flight Control API client from the given config.
func NewFromConfig(config *types.Config) (*client.ClientWithResponses, error) {

	httpClient, err := NewHTTPClientFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewFromConfig: creating HTTP client %w", err)
	}
	ref := client.WithRequestEditorFn(func(ctx context.Context, req *http.Request) error {
		req.Header.Set(middleware.RequestIDHeader, reqid.GetReqID())
		if config.AuthInfo.Token != "" {
			req.Header.Set(common.AuthHeader, fmt.Sprintf("Bearer %s", config.AuthInfo.Token))
		}
		return nil
	})
	return client.NewClientWithResponses(config.Service.Server, client.WithHTTPClient(httpClient), ref)
}

// NewHTTPClientFromConfig returns a new HTTP Client from the given config.
func NewHTTPClientFromConfig(config *types.Config) (*http.Client, error) {
	config = config.DeepCopy()
	if err := config.Flatten(); err != nil {
		return nil, err
	}

	tlsServerName := config.Service.TLSServerName
	if len(tlsServerName) == 0 {
		u, err := url.Parse(config.Service.Server)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing server url: %w", err)
		}
		tlsServerName = u.Hostname()
	}

	tlsConfig, err := CreateTLSConfigFromConfig(config)
	if err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: creating TLS config: %w", err)
	}
	tlsConfig.ServerName = tlsServerName

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}
	return httpClient, nil
}

func CreateTLSConfigFromConfig(config *types.Config) (*tls.Config, error) {
	tlsConfig := tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if err := addServiceCAToTLSConfig(&tlsConfig, config); err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
	}

	if err := addClientCertToTLSConfig(&tlsConfig, config); err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing client cert and key: %w", err)
	}
	return &tlsConfig, nil
}

func addServiceCAToTLSConfig(tlsConfig *tls.Config, config *types.Config) error {
	if len(config.Service.CertificateAuthorityData) > 0 {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return err
		}
		tlsConfig.RootCAs = caPool
	}
	return nil
}

func addClientCertToTLSConfig(tlsConfig *tls.Config, config *types.Config) error {
	if len(config.AuthInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
		if err != nil {
			return err
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}
	return nil
}

// NewGRPCClientFromConfig returns a new gRPC Client from the given config.
func NewGRPCClientFromConfig(config *types.Config, endpoint string) (grpc_v1.RouterServiceClient, error) {
	grpcEndpoint := config.Service.Server
	if endpoint != "" {
		grpcEndpoint = endpoint
	}

	config = config.DeepCopy()
	if err := config.Flatten(); err != nil {
		return nil, err
	}

	tlsConfig := tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if string(config.Service.CertificateAuthorityData) != "" {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
		}
		tlsConfig.RootCAs = caPool
	}

	u, err := url.Parse(grpcEndpoint)
	if err != nil {
		return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing CA certs: %w", err)
	}
	tlsServerName := u.Hostname()
	tlsConfig.ServerName = tlsServerName

	if len(config.AuthInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
		if err != nil {
			return nil, fmt.Errorf("NewHTTPClientFromConfig: parsing client cert and key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}
	// our transport is http, but the grpc library has special encoding for the endpoint
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "http://")
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "https://")
	grpcEndpoint = strings.TrimSuffix(grpcEndpoint, "/")

	client, err := grpc.NewClient(grpcEndpoint, grpc.WithTransportCredentials(credentials.NewTLS(&tlsConfig)))

	if err != nil {
		return nil, fmt.Errorf("NewGRPCClientFromConfig: creating gRPC client: %w", err)
	}

	router := grpc_v1.NewRouterServiceClient(client)

	return router, nil
}

// DefaultFlightctlClientConfigPath returns the default path to the Flight Control client config file.
func DefaultFlightctlClientConfigPath() string {
	return filepath.Join(homedir.HomeDir(), ".config", "flightctl", "client.yaml")
}

func ParseConfigFile(filename string) (*types.Config, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	config := NewDefault()
	if err := yaml.Unmarshal(contents, config); err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	config.SetBaseDir(filepath.Dir(filename))
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return config, config.Flatten()
}

// NewFromConfigFile returns a new Flight Control API client using the config read from the given file.
func NewFromConfigFile(filename string) (*client.ClientWithResponses, error) {
	config, err := ParseConfigFile(filename)
	if err != nil {
		return nil, err
	}
	return NewFromConfig(config)
}

// NewFromConfigFile returns a new Flight Control API client using the config read from the given file.
func NewGrpcClientFromConfigFile(filename string, endpoint string) (grpc_v1.RouterServiceClient, error) {
	contents, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("reading config: %v", err)
	}
	config := NewDefault()
	if err := yaml.Unmarshal(contents, config); err != nil {
		return nil, fmt.Errorf("decoding config: %v", err)
	}
	config.SetBaseDir(filepath.Dir(filename))
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := config.Flatten(); err != nil {
		return nil, err
	}
	return NewGRPCClientFromConfig(config, endpoint)
}

// WriteConfig writes a client config file using the given parameters.
func WriteConfig(filename string, server string, tlsServerName string, ca *crypto.TLSCertificateConfig, client *crypto.TLSCertificateConfig) error {
	caCertPEM, _, err := ca.GetPEMBytes()
	if err != nil {
		return fmt.Errorf("PEM-encoding CA certs: %w", err)
	}

	config := NewDefault()
	config.Service = types.Service{
		Server:                   server,
		TLSServerName:            tlsServerName,
		CertificateAuthorityData: caCertPEM,
	}

	if client != nil {
		clientCertPEM, clientKeyPEM, err := client.GetPEMBytes()
		if err != nil {
			return fmt.Errorf("PEM-encoding client cert and key: %w", err)
		}
		config.AuthInfo = types.AuthInfo{
			ClientCertificateData: clientCertPEM,
			ClientKeyData:         clientKeyPEM,
		}
	}

	return config.Persist(filename)
}
