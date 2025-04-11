package crypto

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/config/ca_config"
	"github.com/flightctl/flightctl/internal/flterrors"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
)

type CABackend interface {
	IssueRequestedCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int, usage []x509.ExtKeyUsage) (*x509.Certificate, error)
	GetCABundleX509() []*x509.Certificate
}

type CAClient struct {
	caBackend CABackend
	Cfg       *ca_config.CAConfigType
}

// EnsureCA() tries to load or generate a CA and connect to it.
// If the CA is successfully loaded or generated it returns a valid CA instance, a flag signifying
// was it loaded or generated and a nil error.
// In case of errors a non-nil error is returned.
func EnsureCA(cfg *ca_config.CAConfigType) (*CAClient, bool, error) {
	caBackend, fresh, err := ensureInternalCA(cfg)
	if err != nil {
		return nil, fresh, err
	}
	ret := &CAClient{
		caBackend: caBackend,
		Cfg:       cfg,
	}
	return ret, fresh, nil
}

func CertStorePath(fileName string, store string) string {
	return filepath.Join(store, fileName)
}

func (caClient *CAClient) BootstrapCNFromName(name string) string {

	cfg := caClient.Cfg
	base := []string{cfg.ClientBootstrapCommonNamePrefix, cfg.DeviceCommonNamePrefix}
	for _, prefix := range append(base, cfg.ExtraAllowedPrefixes...) {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	return caClient.Cfg.ClientBootstrapCommonNamePrefix + name
}

func (caClient *CAClient) CNFromDeviceFingerprint(fingerprint string) (string, error) {
	if len(fingerprint) < 16 {
		return "", errors.New("device fingerprint must have 16 characters at least")
	}
	if strings.HasPrefix(fingerprint, caClient.Cfg.DeviceCommonNamePrefix) {
		return fingerprint, nil
	}
	return caClient.Cfg.DeviceCommonNamePrefix + fingerprint, nil
}

type TLSCertificateConfig oscrypto.TLSCertificateConfig

func (caClient *CAClient) EnsureServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetServerCertificate(certFile, keyFile, hostnames)
	if err != nil {
		certConfig, err = caClient.MakeAndWriteServerCertificate(certFile, keyFile, hostnames, expireDays)
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

func (caClient *CAClient) MakeAndWriteServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error) {
	server, err := caClient.MakeServerCertificate(hostnames, expireDays)
	if err != nil {
		return nil, err
	}
	if err := server.WriteCertConfigFile(certFile, keyFile); err != nil {
		return server, err
	}
	return server, nil
}

func (caClient *CAClient) MakeServerCertificate(hostnames []string, expiryDays int) (*TLSCertificateConfig, error) {
	if len(hostnames) < 1 {
		return nil, fmt.Errorf("at least one hostname must be provided")
	}

	_, serverPrivateKey, _ := NewKeyPair()

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
	serverCrt, err := caClient.IssueRequestedServerCertificateAsX509(csr, expiryDays*86400)
	if err != nil {
		return nil, err
	}
	server := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{serverCrt}, caClient.GetCABundleX509()...),
		Key:   serverPrivateKey,
	}
	return server, nil
}

func (caClient *CAClient) EnsureClientCertificate(certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := caClient.MakeClientCertificate(certFile, keyFile, subjectName, expireDays)
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

func (caClient *CAClient) MakeClientCertificate(certFile, keyFile string, subjectName string, expiryDays int) (*TLSCertificateConfig, error) {

	_, clientPrivateKey, _ := NewKeyPair()

	clientTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: subjectName},
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, clientTemplate, clientPrivateKey)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		return nil, err
	}

	clientCrt, err := caClient.IssueRequestedClientCertificateAsX509(csr, expiryDays*24*3600)
	if err != nil {
		return nil, err
	}
	client := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{clientCrt}, caClient.GetCABundleX509()...),
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
	keyBytes, err := PEMEncodeKey(c.Key)
	if err != nil {
		return nil, nil, err
	}

	return certBytes, keyBytes, nil
}

func (caClient *CAClient) IssueRequestedClientCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return caClient.caBackend.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

func (caClient *CAClient) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	cert, err := caClient.IssueRequestedClientCertificateAsX509(csr, expirySeconds)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", flterrors.ErrSignCert, err)
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", flterrors.ErrEncodeCert, err)
	}

	return certData, nil
}

func (caClient *CAClient) IssueRequestedServerCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return caClient.caBackend.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth})
}
func (caClient *CAClient) IssueRequestedServerCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	cert, err := caClient.IssueRequestedServerCertificateAsX509(csr, expirySeconds)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", flterrors.ErrSignCert, err)
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", flterrors.ErrEncodeCert, err)
	}

	return certData, nil
}

func (caClient *CAClient) GetCABundleX509() []*x509.Certificate {
	return caClient.caBackend.GetCABundleX509()
}

func (caClient *CAClient) GetCABundle() ([]byte, error) {
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
		return false, fmt.Errorf("certificate file missing or unreadable: %s (certificate and key must be provided as a pair)", certPath)
	case !keyExists:
		return false, fmt.Errorf("key file missing or unreadable: %s (certificate and key must be provided as a pair)", keyPath)
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
