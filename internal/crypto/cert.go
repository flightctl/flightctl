package crypto

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/crypto/signer"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
)

type CertOption = func(*x509.Certificate) error

type CABackend interface {
	IssueRequestedCertificateAsX509(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, usage []x509.ExtKeyUsage, opts ...CertOption) (*x509.Certificate, error)
	GetCABundleX509() []*x509.Certificate
}

type CAClient struct {
	caBackend CABackend
	Cfg       *ca.Config
	signers   *signer.CASigners
}

func (caClient *CAClient) Config() *ca.Config {
	return caClient.Cfg
}

func NewCAClient(cfg *ca.Config, caBackend CABackend) *CAClient {
	ca := &CAClient{
		caBackend: caBackend,
		Cfg:       cfg,
	}
	ca.signers = signer.NewCASigners(ca)
	return ca
}

// EnsureCA() tries to load or generate a CA and connect to it.
// If the CA is successfully loaded or generated it returns a valid CA instance, a flag signifying
// was it loaded or generated and a nil error.
// In case of errors a non-nil error is returned.
func EnsureCA(cfg *ca.Config) (*CAClient, bool, error) {
	caBackend, fresh, err := ensureInternalCA(cfg)
	if err != nil {
		return nil, fresh, err
	}
	ca := &CAClient{
		caBackend: caBackend,
		Cfg:       cfg,
	}

	ca.signers = signer.NewCASigners(ca)
	return ca, fresh, nil
}

func (caClient *CAClient) GetSigner(name string) signer.Signer {
	return caClient.signers.GetSigner(name)
}

func (caClient *CAClient) PeerCertificateFromCtx(ctx context.Context) (*x509.Certificate, error) {
	return signer.PeerCertificateFromCtx(ctx)
}

func (caClient *CAClient) PeerCertificateSignerFromCtx(ctx context.Context) signer.Signer {
	peerCertificate, err := signer.PeerCertificateFromCtx(ctx)
	if err != nil {
		return nil
	}

	if name, err := signer.GetSignerNameExtension(peerCertificate); err == nil && name != "" {
		return caClient.GetSigner(name)
	}

	return nil
}

func CertStorePath(fileName string, store string) string {
	return filepath.Join(store, fileName)
}

type TLSCertificateConfig oscrypto.TLSCertificateConfig

func (caClient *CAClient) EnsureServerCertificate(ctx context.Context, certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetServerCertificate(certFile, keyFile, hostnames)
	if err != nil {
		certConfig, err = caClient.MakeAndWriteServerCertificate(ctx, certFile, keyFile, hostnames, expireDays)
		return certConfig, true, err
	}

	return certConfig, false, nil
}

func GetServerCertificate(certFile, keyFile string, hostnames []string) (*TLSCertificateConfig, error) {
	internalServer, err := oscrypto.GetServerCert(certFile, keyFile, sets.NewString(hostnames...))
	if err != nil {
		return nil, err
	}
	server := TLSCertificateConfig(*internalServer)
	return &server, nil
}

func (caClient *CAClient) MakeAndWriteServerCertificate(ctx context.Context, certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error) {
	server, err := caClient.MakeServerCertificate(ctx, hostnames, expireDays)
	if err != nil {
		return nil, err
	}
	if err := server.WriteCertConfigFile(certFile, keyFile); err != nil {
		return server, err
	}
	return server, nil
}

func (caClient *CAClient) MakeServerCertificate(ctx context.Context, hostnames []string, expiryDays int) (*TLSCertificateConfig, error) {
	if len(hostnames) < 1 {
		return nil, fmt.Errorf("at least one hostname must be provided")
	}

	_, serverPrivateKey, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate server key pair: %w", err)
	}

	serverTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: hostnames[0]},
	}
	serverTemplate.IPAddresses, serverTemplate.DNSNames = oscrypto.IPAddressesDNSNames(hostnames)

	raw, err := x509.CreateCertificateRequest(rand.Reader, serverTemplate, serverPrivateKey)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		return nil, err
	}
	serverCrt, err := caClient.IssueRequestedServerCertificate(ctx, csr, expiryDays*86400)
	if err != nil {
		return nil, err
	}
	server := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{serverCrt}, caClient.GetCABundleX509()...),
		Key:   serverPrivateKey,
	}
	return server, nil
}

func (caClient *CAClient) EnsureClientCertificate(ctx context.Context, certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := caClient.MakeClientCertificate(ctx, certFile, keyFile, subjectName, expireDays)
	if err != nil {
		return nil, false, err
	}
	err = certConfig.WriteCertConfigFile(certFile, keyFile)
	return certConfig, err == nil, err // true indicates we wrote the files.
}

func GetClientCertificate(certFile, keyFile string, subjectName string) (*TLSCertificateConfig, error) {
	internalConfig, err := oscrypto.GetTLSCertificateConfig(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	if internalConfig.Certs[0].Subject.CommonName != subjectName {
		return nil, fmt.Errorf("existing client certificate in %s was issued for a different Subject (%s)",
			certFile, subjectName)
	}

	client := TLSCertificateConfig(*internalConfig)
	return &client, nil
}

func (caClient *CAClient) MakeClientCertificate(ctx context.Context, certFile, keyFile string, subjectName string, expiryDays int) (*TLSCertificateConfig, error) {
	_, clientPrivateKey, err := fccrypto.NewKeyPair()
	if err != nil {
		return nil, fmt.Errorf("failed to generate client key pair: %w", err)
	}

	if subjectName == "" {
		subjectName = caClient.Cfg.ClientBootstrapCommonName
	}

	raw, err := fccrypto.MakeCSR(clientPrivateKey.(crypto.Signer), subjectName)
	if err != nil {
		return nil, err
	}

	seconds := expiryDays * 24 * 3600
	if seconds > math.MaxInt32 {
		return nil, fmt.Errorf("expiryDays too large: would overflow int32 seconds")
	}
	expiry := int32(seconds) // #nosec G115 -- safe: bounds already checked above

	x509CSR, err := fccrypto.ParseCSR(raw)
	if err != nil {
		return nil, err
	}

	signReq, err := signer.NewSignRequest(
		caClient.Cfg.DeviceEnrollmentSignerName,
		*x509CSR,
		signer.WithExpirationSeconds(expiry),
		signer.WithResourceName(subjectName),
	)
	if err != nil {
		return nil, err
	}

	signedCert, err := signer.SignVerified(ctx, caClient, signReq)
	if err != nil {
		return nil, err
	}

	client := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{signedCert}, caClient.GetCABundleX509()...),
		Key:   clientPrivateKey,
	}
	return client, nil
}

func GetTLSCertificateConfig(certFile, keyFile string) (*TLSCertificateConfig, error) {
	internalConfig, err := oscrypto.GetTLSCertificateConfig(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	config := TLSCertificateConfig(*internalConfig)
	return &config, nil
}

func (c *TLSCertificateConfig) WriteCertConfigFile(certFile, keyFile string) error {
	internalConfig := oscrypto.TLSCertificateConfig(*c)
	return internalConfig.WriteCertConfigFile(certFile, keyFile)
}

func (c *TLSCertificateConfig) GetPEMBytes() ([]byte, []byte, error) {
	certBytes, err := oscrypto.EncodeCertificates(c.Certs...)
	if err != nil {
		return nil, nil, err
	}
	keyBytes, err := fccrypto.PEMEncodeKey(c.Key)
	if err != nil {
		return nil, nil, err
	}

	return certBytes, keyBytes, nil
}

func (caClient *CAClient) IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) (*x509.Certificate, error) {
	return caClient.caBackend.IssueRequestedCertificateAsX509(ctx, csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, opts...)
}

func (caClient *CAClient) IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...CertOption) (*x509.Certificate, error) {
	return caClient.caBackend.IssueRequestedCertificateAsX509(ctx, csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, opts...)
}

func (caClient *CAClient) GetCABundleX509() []*x509.Certificate {
	return caClient.caBackend.GetCABundleX509()
}

func (caClient *CAClient) GetCABundle() ([]byte, error) {
	// If CABundleFile is configured, read it directly
	if caClient.Cfg.InternalConfig.CABundleFile != "" {
		caBundlePath := CertStorePath(caClient.Cfg.InternalConfig.CABundleFile, caClient.Cfg.InternalConfig.CertStore)
		caBundleBytes, err := os.ReadFile(caBundlePath)
		if err != nil {
			return nil, fmt.Errorf("reading ca-bundle from %s: %w", caBundlePath, err)
		}
		return caBundleBytes, nil
	}
	// Fallback to using loaded certificates
	certs := caClient.GetCABundleX509()
	return oscrypto.EncodeCertificates(certs...)
}

// CanReadCertAndKey checks if both the certificate and key files exist and are readable.
// Returns true if both files are accessible, false if neither exists, and an error if one is missing.
func CanReadCertAndKey(certPath, keyPath string) (bool, error) {
	certExists := isFileReadable(certPath)
	keyExists := isFileReadable(keyPath)

	switch {
	case !certExists && !keyExists:
		return false, nil
	case !certExists:
		return false, fmt.Errorf("certificate file missing or unreadable: \"%s\" (certificate and key must be provided as a pair)", certPath)
	case !keyExists:
		return false, fmt.Errorf("key file missing or unreadable: \"%s\" (certificate and key must be provided as a pair)", keyPath)
	default:
		return true, nil
	}
}

// isFileReadable checks if the given file path exists and is readable.
func isFileReadable(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	return true
}
