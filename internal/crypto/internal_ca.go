package crypto

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"os"
	"time"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
)


type CA struct {
	Config *TLSCertificateConfig

	SerialGenerator oscrypto.SerialGenerator
}

func EnsureCA(certFile, keyFile, serialFile, subjectName string, expireDays int) (*CA, bool, error) {
	if ca, err := GetCA(certFile, keyFile, serialFile); err == nil {
		return ca, false, err
	}
	ca, err := MakeSelfSignedCA(certFile, keyFile, serialFile, subjectName, expireDays)
	return ca, true, err
}

func GetCA(certFile, keyFile, serialFile string) (*CA, error) {
	ca, err := oscrypto.GetCA(certFile, keyFile, serialFile)
	if err != nil {
		return nil, err
	}
	config := TLSCertificateConfig(*ca.Config)
	return &CA{Config: &config, SerialGenerator: ca.SerialGenerator}, err
}

func MakeSelfSignedCA(certFile, keyFile, serialFile, subjectName string, expiryDays int) (*CA, error) {
	caConfig, err := makeSelfSignedCAConfig(
		pkix.Name{CommonName: subjectName},
		time.Duration(expiryDays)*24*time.Hour,
	)
	if err != nil {
		return nil, err
	}
	if err = caConfig.WriteCertConfigFile(certFile, keyFile); err != nil {
		return nil, err
	}

	var serialGenerator oscrypto.SerialGenerator
	if len(serialFile) > 0 {
		// create / overwrite the serial file with a zero padded hex value (ending in a newline to have a valid file)
		if err := os.WriteFile(serialFile, []byte("00\n"), 0600); err != nil {
			return nil, err
		}
		serialGenerator, err = oscrypto.NewSerialFileGenerator(serialFile)
		if err != nil {
			return nil, err
		}
	} else {
		serialGenerator = &oscrypto.RandomSerialGenerator{}
	}

	config := TLSCertificateConfig(*caConfig)
	return &CA{
		SerialGenerator: serialGenerator,
		Config:          &config,
	}, nil
}

func makeSelfSignedCAConfig(subject pkix.Name, caLifetime time.Duration) (*oscrypto.TLSCertificateConfig, error) {
	rootcaPublicKey, rootcaPrivateKey, publicKeyHash, err := NewKeyPairWithHash()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	rootcaTemplate := &x509.Certificate{
		Subject: subject,

		SignatureAlgorithm: x509.ECDSAWithSHA256,

		NotBefore: now.Add(-1 * time.Second),
		NotAfter:  now.Add(caLifetime),

		SerialNumber: randomSerial(),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
		IsCA:                  true,

		AuthorityKeyId: publicKeyHash,
		SubjectKeyId:   publicKeyHash,
	}
	rootcaCert, err := signCertificate(rootcaTemplate, rootcaPublicKey, rootcaTemplate, rootcaPrivateKey)
	if err != nil {
		return nil, err
	}
	caConfig := &oscrypto.TLSCertificateConfig{
		Certs: []*x509.Certificate{rootcaCert},
		Key:   rootcaPrivateKey,
	}
	return caConfig, nil
}

func (ca *CA) signCertificate(template *x509.Certificate, requestKey crypto.PublicKey) (*x509.Certificate, error) {
	// Increment and persist serial
	serial, err := ca.SerialGenerator.Next(template)
	if err != nil {
		return nil, err
	}
	template.SerialNumber = big.NewInt(serial)
	return signCertificate(template, requestKey, ca.Config.Certs[0], ca.Config.Key)
}


func signCertificate(template *x509.Certificate, requestKey crypto.PublicKey, issuer *x509.Certificate, issuerKey crypto.PrivateKey) (*x509.Certificate, error) {
	derBytes, err := x509.CreateCertificate(rand.Reader, template, issuer, requestKey, issuerKey)
	if err != nil {
		return nil, err
	}
	certs, err := x509.ParseCertificates(derBytes)
	if err != nil {
		return nil, err
	}
	if len(certs) != 1 {
		return nil, errors.New("expected a single certificate")
	}
	return certs[0], nil
}


// IssueRequestedClientCertificate issues a client certificate based on the provided
// Certificate Signing Request (CSR) and the desired expiration time in seconds.
// This currently processes both enrollment cert and management cert signing requests, which both are signed
// by the FC service's internal CA instance named 'ca'.


func (ca *CA) IssueRequestedCertificateAsX509(csr *x509.CertificateRequest, expirySeconds int, usage []x509.ExtKeyUsage) (*x509.Certificate, error) {
	now := time.Now()
	template := &x509.Certificate{
		Subject: csr.Subject,

		Signature:          csr.Signature,
		SignatureAlgorithm: csr.SignatureAlgorithm,

		PublicKey:          csr.PublicKey,
		PublicKeyAlgorithm: csr.PublicKeyAlgorithm,
		IPAddresses: csr.IPAddresses,
		DNSNames: csr.DNSNames,

		Issuer: ca.Config.Certs[0].Subject,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expirySeconds) * time.Second),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usage,
		BasicConstraintsValid: true,

		AuthorityKeyId: ca.Config.Certs[0].SubjectKeyId,
	}
	return ca.signCertificate(template, csr.PublicKey)
}
