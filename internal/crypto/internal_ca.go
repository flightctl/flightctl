package crypto

import (
	"crypto"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

type internalCA struct {
	Id string
	Config *TLSCertificateConfig
	SerialGenerator oscrypto.SerialGenerator
}

func (ca *internalCA) GetId() string {
	return ca.Id
}

func (ca *internalCA) GetConfig() *TLSCertificateConfig {
	return ca.Config
}

func (ca *internalCA) EnsureServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetServerCert(certFile, keyFile, hostnames)
	if err != nil {
		certConfig, err = ca.MakeAndWriteServerCert(certFile, keyFile, hostnames, expireDays)
		return certConfig, true, err
	}

	return certConfig, false, nil
}

func (ca *internalCA) MakeAndWriteServerCert(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error) {
	server, err := ca.MakeServerCert(hostnames, expireDays)
	if err != nil {
		return nil, err
	}
	if err := server.WriteCertConfigFile(certFile, keyFile); err != nil {
		return nil, err
	}
	return server, nil
}

func (ca *internalCA) MakeServerCert(hostnames []string, expiryDays int, fns ...CertificateExtensionFunc) (*TLSCertificateConfig, error) {
	if len(hostnames) < 1 {
		return nil, fmt.Errorf("at least one hostname must be provided")
	}

	serverPublicKey, serverPrivateKey, publicKeyHash, _ := NewKeyPairWithHash()

	now := time.Now()
	serverTemplate := &x509.Certificate{
		Subject: pkix.Name{CommonName: hostnames[0]},

		SignatureAlgorithm: x509.ECDSAWithSHA256,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expiryDays) * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
		SubjectKeyId:   publicKeyHash,
	}
	serverTemplate.IPAddresses, serverTemplate.DNSNames = oscrypto.IPAddressesDNSNames(hostnames)
	for _, fn := range fns {
		if err := fn(serverTemplate); err != nil {
			return nil, err
		}
	}

	serverCrt, err := ca.signCertificate(serverTemplate, serverPublicKey)
	if err != nil {
		return nil, err
	}
	server := &TLSCertificateConfig{
		Certs: append([]*x509.Certificate{serverCrt}, ca.Config.Certs...),
		Key:   serverPrivateKey,
	}
	return server, nil
}

func (ca *internalCA) signCertificate(template *x509.Certificate, requestKey crypto.PublicKey) (*x509.Certificate, error) {
	// Increment and persist serial
	serial, err := ca.SerialGenerator.Next(template)
	if err != nil {
		return nil, err
	}
	template.SerialNumber = big.NewInt(serial)
	return signCertificate(template, requestKey, ca.Config.Certs[0], ca.Config.Key)
}

func (ca *internalCA) EnsureClientCertificate(certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetClientCertificate(certFile, keyFile, subjectName)
	if err != nil {
		certConfig, err = ca.MakeClientCertificate(certFile, keyFile, subjectName, expireDays)
		return certConfig, true, err // true indicates we wrote the files.
	}
	return certConfig, false, nil
}

func (ca *internalCA) MakeClientCertificate(certFile, keyFile string, subject string, expiryDays int) (*TLSCertificateConfig, error) {
	if err := os.MkdirAll(filepath.Dir(certFile), os.FileMode(0755)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), os.FileMode(0755)); err != nil {
		return nil, err
	}

	clientPublicKey, clientPrivateKey, isNewKey, err := EnsureKey(keyFile)
	if err != nil {
		return nil, err
	}
	clientPublicKeyHash, err := HashPublicKey(clientPublicKey)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	clientTemplate := &x509.Certificate{

		SignatureAlgorithm: x509.ECDSAWithSHA256,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expiryDays) * 24 * time.Hour),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
		SubjectKeyId:   clientPublicKeyHash,
	}
	if subject != "" {
		clientTemplate.Subject = pkix.Name{CommonName: subject}
	}
	clientCrt, err := ca.signCertificate(clientTemplate, clientPublicKey)
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

func (ca *internalCA) IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error) {
	now := time.Now()
	template := &x509.Certificate{
		Subject: csr.Subject,

		Signature:          csr.Signature,
		SignatureAlgorithm: csr.SignatureAlgorithm,

		PublicKey:          csr.PublicKey,
		PublicKeyAlgorithm: csr.PublicKeyAlgorithm,

		Issuer: ca.Config.Certs[0].Subject,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expirySeconds) * time.Second),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
	}
	cert, err := ca.signCertificate(template, csr.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrSignCert, err.Error())
	}
	certData, err := oscrypto.EncodeCertificates(cert)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", flterrors.ErrEncodeCert, err)
	}

	return certData, nil
}

