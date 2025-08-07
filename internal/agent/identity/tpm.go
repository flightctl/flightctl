package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_client "github.com/flightctl/flightctl/internal/api/client/agent"
	base_client "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/internal/tpm"
	"github.com/flightctl/flightctl/pkg/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

var _ Provider = (*tpmProvider)(nil)

// tpmProvider implements identity management using TPM-based keys
type tpmProvider struct {
	client          *tpm.Client
	log             *log.PrefixLogger
	deviceName      string
	certificateData []byte
}

// newTPMProvider creates a new TPM-based identity provider
func newTPMProvider(
	client *tpm.Client,
	log *log.PrefixLogger,
) *tpmProvider {
	return &tpmProvider{
		client: client,
		log:    log,
	}
}

func (t *tpmProvider) Initialize(ctx context.Context) error {
	publicKey := t.client.Public()
	if publicKey == nil {
		return fmt.Errorf("failed to get public key from TPM")
	}

	var err error
	t.deviceName, err = generateDeviceName(publicKey)
	if err != nil {
		return err
	}

	if err := t.client.UpdateNonce(make([]byte, 8)); err != nil {
		t.log.Warnf("Failed to update TPM nonce: %v", err)
	}
	return nil
}

func (t *tpmProvider) GetDeviceName() (string, error) {
	return t.deviceName, nil
}

func (t *tpmProvider) GenerateCSR(deviceName string) ([]byte, error) {
	// Use default qualifying data (nonce) for attestation freshness
	qualifyingData := make([]byte, 8)
	return t.client.MakeCSR(deviceName, qualifyingData)
}

func (t *tpmProvider) StoreCertificate(certPEM []byte) error {
	t.certificateData = certPEM
	return nil
}

func (t *tpmProvider) HasCertificate() bool {
	return len(t.certificateData) > 0
}

func (t *tpmProvider) createCertificate() (*tls.Certificate, error) {
	if t.client == nil {
		return nil, fmt.Errorf("TPM client not initialized")
	}
	if t.certificateData == nil {
		return nil, fmt.Errorf("no certificate data available for TPM authentication - device needs enrollment")
	}
	signer := t.client.GetSigner()
	// parse the certificate from PEM block
	certBlock, _ := pem.Decode(t.certificateData)
	if certBlock == nil {
		return nil, fmt.Errorf("failed to decode certificate PEM")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse certificate: %w", err)
	}

	// create TLS certificate using the TPM private key and the parsed certificate
	tlsCert := &tls.Certificate{
		Certificate: [][]byte{cert.Raw},
		PrivateKey:  signer,
	}
	return tlsCert, nil
}

func (t *tpmProvider) CreateManagementClient(config *base_client.Config, metricsCallback client.RPCMetricsCallback) (client.Management, error) {
	tlsCert, err := t.createCertificate()
	if err != nil {
		return nil, err
	}
	configCopy := config.DeepCopy()
	if err := configCopy.Flatten(); err != nil {
		return nil, err
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*tlsCert},
		MinVersion:   tls.VersionTLS13,
	}

	if configCopy.Service.CertificateAuthorityData != nil {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(configCopy.Service.CertificateAuthorityData)
		tlsConfig.RootCAs = caCertPool
	}

	if configCopy.Service.TLSServerName != "" {
		tlsConfig.ServerName = configCopy.Service.TLSServerName
	} else {
		u, err := url.Parse(configCopy.Service.Server)
		if err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	clientWithResponses, err := agent_client.NewClientWithResponses(configCopy.Service.Server, agent_client.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("creating client: %w", err)
	}

	managementClient := client.NewManagement(clientWithResponses, metricsCallback)
	return managementClient, nil
}

func (t *tpmProvider) CreateGRPCClient(config *base_client.Config) (grpc_v1.RouterServiceClient, error) {
	tlsCert, err := t.createCertificate()
	if err != nil {
		return nil, err
	}

	configCopy := config.DeepCopy()
	if err := configCopy.Flatten(); err != nil {
		return nil, err
	}

	grpcEndpoint := configCopy.Service.Server

	tlsConfig := &tls.Config{
		Certificates:       []tls.Certificate{*tlsCert},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: configCopy.Service.InsecureSkipVerify, //nolint:gosec
	}

	if configCopy.Service.CertificateAuthorityData != nil {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(configCopy.Service.CertificateAuthorityData)
		tlsConfig.RootCAs = caCertPool
	}

	if configCopy.Service.TLSServerName != "" {
		tlsConfig.ServerName = configCopy.Service.TLSServerName
	} else {
		u, err := url.Parse(grpcEndpoint)
		if err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	// our transport is http, but the grpc library has special encoding for the endpoint
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "http://")
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "https://")
	grpcEndpoint = strings.TrimSuffix(grpcEndpoint, "/")

	grpcClient, err := grpc.NewClient(grpcEndpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                30 * time.Second, // Send keepalive ping every 30s
			Timeout:             10 * time.Second, // Wait 10s for server response
			PermitWithoutStream: true,             // Send even if no active RPCs
		}))
	if err != nil {
		return nil, fmt.Errorf("creating gRPC client: %w", err)
	}

	router := grpc_v1.NewRouterServiceClient(grpcClient)
	return router, nil
}

func (t *tpmProvider) WipeCredentials() error {
	// clear certificate data from memory
	t.certificateData = nil
	if err := t.client.Clear(); err != nil {
		return fmt.Errorf("clearing TPM client: %w", err)
	}
	t.log.Info("Wiped TPM-stored certificate data from memory")
	return nil
}

func (t *tpmProvider) Close(ctx context.Context) error {
	if t.client != nil {
		return t.client.Close(ctx)
	}
	return nil
}
