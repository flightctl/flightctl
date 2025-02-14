package crypto

import (
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Wraps openshift/library-go/pkg/crypto to use ECDSA and simplify the interface
const ClientBootstrapCommonName = "client-enrollment"
const ClientBootstrapCommonNamePrefix = "client-enrollment-"
const AdminCommonName = "flightctl-admin"
const DeviceCommonNamePrefix = "device:"

func BootstrapCNFromName(name string) string {
	return ClientBootstrapCommonNamePrefix + name
}

func CNFromDeviceFingerprint(fingerprint string) (string, error) {
	if len(fingerprint) < 16 {
		return "", errors.New("device fingerprint must have 16 characters at least")
	}
	return DeviceCommonNamePrefix + fingerprint, nil
}

type TLSCertificateConfig oscrypto.TLSCertificateConfig


type CA struct {
	ca *CABackend
}

func EnsureCA(certFile, keyFile, serialFile, subjectName string, expireDays int) (*CA, bool, error) {

	caBackend, generated, err := EnsureInternalCA(certFile, keyFile, serialFile, subjectName, expireDays)

	if err != nil {
		return nil, generated, err
	}

	ca := &CA {
		ca:	caBackend,
	}

	return ca, generated, err
}


func (caClient *CA) IssueRequestedClientCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return caClient.ca.IssueRequestedClientCertificateAsX509(csr, expirySeconds)
}

func (caClient *CA) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	return caClient.ca.IssueRequestedClientCertificate(csr, expirySeconds)
}

func (caClient *CA) IssueRequestedServerCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int) (*x509.Certificate, error) {
	return caClient.ca.IssueRequestedServerCertificateAsX509(csr, expirySeconds)
}

func (caClient *CA) IssueRequestedServerCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	return caClient.ca.IssueRequestedServerCertificate(csr, expirySeconds)
}

func (caClient *CA) GetCABundle() ([]byte, error) {

	certs := caClient.ca.GetCABundleX509()

	return oscrypto.EncodeCertificates(certs...)
}

func (caClient *CA) GetCABundleX509() ([]*x509.Certificate) {
	return caClient.ca.GetCABundleX509()
}


func (caClient *CA) EnsureServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetServerCert(certFile, keyFile, hostnames)
	if err != nil {
		certConfig, err = caClient.MakeAndWriteServerCert(certFile, keyFile, hostnames, expireDays)
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

func (caClient *CA) MakeAndWriteServerCert(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error) {
	server, err := caClient.MakeServerCert(hostnames, expireDays)
	if err != nil {
		return nil, err
	}
	if err := server.WriteCertConfigFile(certFile, keyFile); err != nil {
		return server, err
	}
	return server, nil
}


func (caClient *CA) MakeServerCert(hostnames []string, expiryDays int) (*TLSCertificateConfig, error) {
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

	if csr.Signature == nil || csr.PublicKey == nil {
		return nil, fmt.Errorf("Generating Invalid CSR, internal error")
	}

	serverCrt, err := caClient.IssueRequestedServerCertificateAsX509(csr, expiryDays * 24 * 3600)
	if err != nil {
		return nil, err
	}

	server := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{serverCrt}, caClient.GetCABundleX509()...),
		Key:   serverPrivateKey,
	}
	return server, nil
}


func (caClient *CA) EnsureClientCertificate(certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetClientCertificate(certFile, keyFile, subjectName)
	if err != nil {
		certConfig, err = caClient.MakeClientCertificate(certFile, keyFile, subjectName, expireDays)
		return certConfig, true, err // true indicates we wrote the files.
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

func (caClient *CA) MakeClientCertificate(certFile, keyFile string, subject string, expiryDays int) (*TLSCertificateConfig, error) {
	if err := os.MkdirAll(filepath.Dir(certFile), os.FileMode(0755)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), os.FileMode(0755)); err != nil {
		return nil, err
	}

	_, clientPrivateKey, isNewKey, err := EnsureKey(keyFile)
	if err != nil {
		return nil, err
	}
	clientTemplate := &x509.CertificateRequest{
		SignatureAlgorithm: x509.ECDSAWithSHA256,
	}
	if subject != "" {
		clientTemplate.Subject = pkix.Name{CommonName: subject}
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, clientTemplate, clientPrivateKey)
	if err != nil {
		return nil, err
	}

	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		return nil, err
	}

	if csr.Signature == nil || csr.PublicKey == nil {
		return nil, fmt.Errorf("Generating Invalid CSR, internal error")
	}

	clientCrt, err := caClient.IssueRequestedClientCertificateAsX509(csr, expiryDays * 24 * 3600)
	if err != nil {
		return nil, err
	}

	certData, err := oscrypto.EncodeCertificates(clientCrt)
	if err != nil {
		return nil, err
	}
	keyData, err := PEMEncodeKey(clientPrivateKey)
	if err != nil {
		return nil, err
	}

	if err = os.WriteFile(certFile, certData, os.FileMode(0644)); err != nil {
		return nil, err
	}
	if isNewKey {
		if err = os.WriteFile(keyFile, keyData, os.FileMode(0600)); err != nil {
			return nil, err
		}
	}

	return GetTLSCertificateConfig(certFile, keyFile)
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
