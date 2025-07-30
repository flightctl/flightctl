package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/binary"
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
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/flightctl/flightctl/pkg/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
)

var _ Provider = (*tpmProvider)(nil)
var _ TPMCapable = (*tpmProvider)(nil)
var _ TPMProvider = (*tpmProvider)(nil)

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
	var err error
	t.deviceName, err = generateDeviceName(t.client.Public())
	if err != nil {
		return err
	}

	// Generate a cryptographically secure random nonce for TPM operations
	// Per TPM 2.0 specification Part 1 Rev 1.59, Section 10.4.2: "The nonce provides
	// replay protection and ensures attestation freshness"
	nonce := make([]byte, 8) // MinNonceLength from TPM client
	if _, err := rand.Read(nonce); err != nil {
		t.log.Warnf("Failed to generate secure nonce, using fallback: %v", err)
		// Fallback to zero nonce if crypto/rand fails (should be extremely rare)
		nonce = make([]byte, 8)
	}

	if err := t.client.UpdateNonce(nonce); err != nil {
		t.log.Warnf("Failed to update TPM nonce: %v", err)
	}
	return nil
}

func (t *tpmProvider) GetDeviceName() (string, error) {
	return t.deviceName, nil
}

func (t *tpmProvider) GenerateCSR(deviceName string) ([]byte, error) {
	signer := t.client.GetSigner()
	return fccrypto.MakeCSR(signer, deviceName)
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

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*tlsCert},
		MinVersion:   tls.VersionTLS13,
	}

	if config.Service.CertificateAuthorityData != nil {
		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(config.Service.CertificateAuthorityData)
		tlsConfig.RootCAs = caCertPool
	}

	if config.Service.TLSServerName != "" {
		tlsConfig.ServerName = config.Service.TLSServerName
	} else {
		u, err := url.Parse(config.Service.Server)
		if err == nil {
			tlsConfig.ServerName = u.Hostname()
		}
	}

	httpClient := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
		},
	}

	clientWithResponses, err := agent_client.NewClientWithResponses(config.Service.Server, agent_client.WithHTTPClient(httpClient))
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
	t.log.Info("Wiped TPM-stored certificate data from memory")
	return nil
}

// GetEKCert returns the EK certificate in PEM format
func (t *tpmProvider) GetEKCert() ([]byte, error) {
	der, err := t.client.EndorsementKeyCert()
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: der,
	}), nil
}

// GetCertifyCert returns the certify certificate in PEM format
func (t *tpmProvider) GetCertifyCert() ([]byte, error) {
	pub := t.client.Public()
	return fccrypto.PEMEncodePublicKey(pub)
}

// GetTPMCertifyCert returns the TPM attestation report that proves the LDevID was created by the TPM
// Now uses TCG compliant attestation with EK->LAK and EK->LDevID certify operations
func (t *tpmProvider) GetTPMCertifyCert() ([]byte, error) {
	if t.client == nil {
		return nil, fmt.Errorf("TPM client not initialized")
	}

	// Generate cryptographically secure qualifying data (nonce) for attestation freshness
	// Per TCG TPM 2.0 Keys for Device Identity and Attestation v1.0 Rev 12, Section 5.2:
	// "The qualifying data (nonce) provides replay protection and ensures attestation freshness"
	qualifyingData := make([]byte, 16) // Use 16 bytes for enhanced security
	if _, err := rand.Read(qualifyingData); err != nil {
		return nil, fmt.Errorf("failed to generate secure qualifying data: %w", err)
	}

	// Use the new TCG compliant attestation method
	return t.client.GetTCGAttestationBytes(qualifyingData)
}

// GetTCGAttestation returns the complete TCG compliant attestation bundle
// This implements TCG TPM 2.0 Keys for Device Identity and Attestation v1.0 Rev 12
// Section 5: Device Identity and Attestation Architecture
func (t *tpmProvider) GetTCGAttestation() (*tpm.AttestationBundle, error) {
	if t.client == nil {
		return nil, fmt.Errorf("TPM client not initialized")
	}

	nonce := make([]byte, 16) // Use 16 bytes for enhanced security
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate secure qualifying data: %w", err)
	}

	return t.client.GetTCGAttestation(nonce)
}

// GetTPM returns the TPM provider (itself) since this provider supports TPM functionality
func (t *tpmProvider) GetTPM() (TPMProvider, bool) {
	return t, true
}

func (t *tpmProvider) Close(ctx context.Context) error {
	if t.client != nil {
		return t.client.Close(ctx)
	}
	return nil
}

func ParseEKCertificate(ekCert []byte) (*x509.Certificate, error) {
	var isWrapped bool

	// TCG PC Client Platform TPM Profile Specification v1.05 Rev 14, Section 7.3.2
	// specifies a prefix when storing a certificate in NVRAM. We look
	// for and unwrap the certificate if its present.
	if len(ekCert) > tpm.NVRAMCertHeaderLength && bytes.Equal(ekCert[:tpm.NVRAMCertPrefixLength], tpm.NVRAMCertPrefix) {
		certLen := int(binary.BigEndian.Uint16(ekCert[tpm.NVRAMCertPrefixLength:tpm.NVRAMCertHeaderLength]))
		if len(ekCert) < certLen+tpm.NVRAMCertHeaderLength {
			return nil, fmt.Errorf("parsing nvram header: ekCert size %d smaller than specified cert length %d", len(ekCert), certLen)
		}
		ekCert = ekCert[tpm.NVRAMCertHeaderLength : certLen+tpm.NVRAMCertHeaderLength]
		isWrapped = true
	}

	cert, err := x509.ParseCertificate(ekCert)
	if err != nil {
		// if DER parsing fails, try PEM
		if !isWrapped {
			block, _ := pem.Decode(ekCert)
			if block != nil && block.Type == "CERTIFICATE" {
				cert, err = x509.ParseCertificate(block.Bytes)
				if err == nil {
					return cert, nil
				}
			}
		}
		return nil, fmt.Errorf("failed to parse EK certificate as DER or PEM: %w", err)
	}

	return cert, nil
}

// ValidateEKCertificateChain validates an EK certificate chain while handling TPM-specific critical extensions
func ValidateEKCertificateChain(cert *x509.Certificate, roots *x509.CertPool) error {
	// TPM certificates often contain critical extensions that Go's x509 library doesn't recognize.

	// store original unhandled extensions
	originalUnhandled := make([]asn1.ObjectIdentifier, len(cert.UnhandledCriticalExtensions))
	copy(originalUnhandled, cert.UnhandledCriticalExtensions)

	removeKnownTPMExtensions(cert)

	// attempt standard validation with full security checks
	opts := x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	}
	_, err := cert.Verify(opts)

	// restore original unhandled extensions list
	cert.UnhandledCriticalExtensions = originalUnhandled

	return err
}

// removeKnownTPMExtensions temporarily removes known TPM critical extensions
func removeKnownTPMExtensions(cert *x509.Certificate) {
	// define TPM-specific critical extensions that we can safely ignore during validation
	// these are commonly found in TPM EK certificates and contain vendor-specific data
	knownTPMExtensionOIDs := []asn1.ObjectIdentifier{
		{2, 5, 29, 17}, // Subject Alternative Name (with TPM-specific directoryName content)
		{2, 5, 29, 19}, // Basic Constraints (sometimes with vendor-specific values)
	}

	// filter out known TPM extensions from unhandled critical extensions
	filtered := cert.UnhandledCriticalExtensions[:0] // Reuse slice capacity
	for _, unhandledOID := range cert.UnhandledCriticalExtensions {
		isKnownExt := false
		for _, knownOID := range knownTPMExtensionOIDs {
			if unhandledOID.Equal(knownOID) {
				isKnownExt = true
				break
			}
		}
		if !isKnownExt {
			filtered = append(filtered, unhandledOID)
		}
	}
	cert.UnhandledCriticalExtensions = filtered
}
