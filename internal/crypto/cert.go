package crypto

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/flightctl/flightctl/internal/flterrors"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Wraps openshift/library-go/pkg/crypto to use ECDSA and simplify the interface
const ClientBootstrapCommonName = "client-enrollment"
const ClientBootstrapCommonNamePrefix = "client-enrollment-"
const AdminCommonName = "flightctl-admin"
const DeviceCommonNamePrefix = "device:"

var allowedPrefixes = [...]string{
	DeviceCommonNamePrefix,
	ClientBootstrapCommonNamePrefix,
}

func BootstrapCNFromName(name string) string {

	for _, prefix := range allowedPrefixes {
		if strings.HasPrefix(name, prefix) {
			return name
		}
	}
	return ClientBootstrapCommonNamePrefix + name
}

func CNFromDeviceFingerprint(fingerprint string) (string, error) {
	if len(fingerprint) < 16 {
		return "", errors.New("device fingerprint must have 16 characters at least")
	}
	if strings.HasPrefix(fingerprint, DeviceCommonNamePrefix) {
		return fingerprint, nil
	}
	return DeviceCommonNamePrefix + fingerprint, nil
}

type TLSCertificateConfig oscrypto.TLSCertificateConfig

func (ca *CA) EnsureServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetServerCert(certFile, keyFile, hostnames)
	if err != nil {
		certConfig, err = ca.MakeAndWriteServerCert(certFile, keyFile, hostnames, expireDays)
		return certConfig, true, err
	}

	return certConfig, false, nil
}

func GetServerCert(certFile, keyFile string, hostnames []string) (*TLSCertificateConfig, error) {
	internalServer, err := oscrypto.GetServerCert(certFile, keyFile, sets.NewString(hostnames...))
	if err != nil {
		return nil, err
	}
	server := TLSCertificateConfig(*internalServer)
	return &server, nil
}

func (ca *CA) MakeAndWriteServerCert(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error) {
	server, err := ca.MakeServerCert(hostnames, expireDays)
	if err != nil {
		return nil, err
	}
	if err := server.WriteCertConfigFile(certFile, keyFile); err != nil {
		return server, err
	}
	return server, nil
}

func (ca *CA) MakeServerCert(hostnames []string, expiryDays int) (*TLSCertificateConfig, error) {
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
	serverCrt, err := ca.IssueRequestedServerCertificateAsX509(csr, expiryDays*86400)
	if err != nil {
		return nil, err
	}
	server := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{serverCrt}, ca.Config.Certs...),
		Key:   serverPrivateKey,
	}
	return server, nil
}

func (ca *CA) EnsureClientCertificate(certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetClientCertificate(certFile, keyFile, subjectName)
	if err != nil {
		certConfig, err := ca.MakeClientCert(certFile, keyFile, subjectName, expireDays)
		if err != nil {
			return nil, false, err
		}
		err = certConfig.WriteCertConfigFile(certFile, keyFile)
		return certConfig, err == nil, err // true indicates we wrote the files.
	}
	return certConfig, false, nil
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

func (ca *CA) MakeClientCert(certFile, keyFile string, subjectName string, expiryDays int) (*TLSCertificateConfig, error) {

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

	clientCrt, err := ca.IssueRequestedClientCertificateAsX509(csr, expiryDays*24*3600)
	if err != nil {
		return nil, err
	}
	client := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{clientCrt}, ca.Config.Certs...),
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

func (ca *CA) IssueRequestedClientCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return ca.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
}

func (ca *CA) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	cert, err := ca.IssueRequestedClientCertificateAsX509(csr, expirySeconds)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignCert, err.Error())
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err.Error())
	}

	return certData, nil
}

func (ca *CA) IssueRequestedServerCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return ca.IssueRequestedCertificateAsX509(csr, expirySeconds, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth})
}
func (ca *CA) IssueRequestedServerCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	cert, err := ca.IssueRequestedServerCertificateAsX509(csr, expirySeconds)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignCert, err.Error())
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err.Error())
	}

	return certData, nil
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
