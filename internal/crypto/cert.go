package crypto

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apiserver/pkg/authentication/user"
)

// Wraps openshift/library-go/pkg/crypto to use ECDSA and simplify the interface

type TLSCertificateConfig oscrypto.TLSCertificateConfig
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
	if err := caConfig.WriteCertConfigFile(certFile, keyFile); err != nil {
		return nil, err
	}

	var serialGenerator oscrypto.SerialGenerator
	if len(serialFile) > 0 {
		// create / overwrite the serial file with a zero padded hex value (ending in a newline to have a valid file)
		if err := os.WriteFile(serialFile, []byte("00\n"), 0644); err != nil {
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

// func (ca *CA) EnsureSubCA(certFile, keyFile, serialFile, name string, expireDays int) (*CA, bool, error) {
// 	if subCA, err := GetCA(certFile, keyFile, serialFile); err == nil {
// 		return subCA, false, err
// 	}
// 	subCA, err := ca.MakeAndWriteSubCA(certFile, keyFile, serialFile, name, expireDays)
// 	return subCA, true, err
// }

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

type CertificateExtensionFunc func(*x509.Certificate) error

func (ca *CA) MakeServerCert(hostnames []string, expiryDays int, fns ...CertificateExtensionFunc) (*TLSCertificateConfig, error) {
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
		SerialNumber: big.NewInt(1),

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

func (ca *CA) signCertificate(template *x509.Certificate, requestKey crypto.PublicKey) (*x509.Certificate, error) {
	// Increment and persist serial
	serial, err := ca.SerialGenerator.Next(template)
	if err != nil {
		return nil, err
	}
	template.SerialNumber = big.NewInt(serial)
	return signCertificate(template, requestKey, ca.Config.Certs[0], ca.Config.Key)
}

func (ca *CA) EnsureClientCertificate(certFile, keyFile string, subject string, expireDays int) (*TLSCertificateConfig, bool, error) {
	certConfig, err := GetClientCertificate(certFile, keyFile, subject)
	if err != nil {
		certConfig, err = ca.MakeClientCertificate(certFile, keyFile, subject, expireDays)
		return certConfig, true, err // true indicates we wrote the files.
	}
	return certConfig, false, nil
}

func GetClientCertificate(certFile, keyFile string, subject string) (*TLSCertificateConfig, error) {
	internalClient, err := oscrypto.GetClientCertificate(certFile, keyFile, &user.DefaultInfo{Name: subject})
	if err != nil {
		return nil, err
	}
	client := TLSCertificateConfig(*internalClient)
	return &client, nil
}

func (ca *CA) MakeClientCertificate(certFile, keyFile string, subject string, expiryDays int) (*TLSCertificateConfig, error) {
	if err := os.MkdirAll(filepath.Dir(certFile), os.FileMode(0755)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyFile), os.FileMode(0755)); err != nil {
		return nil, err
	}

	clientPublicKey, clientPrivateKey, _ := NewKeyPair()

	now := time.Now()
	clientTemplate := &x509.Certificate{
		Subject: pkix.Name{CommonName: subject},

		SignatureAlgorithm: x509.ECDSAWithSHA256,

		NotBefore:    now.Add(-1 * time.Second),
		NotAfter:     now.Add(time.Duration(expiryDays) * 24 * time.Hour),
		SerialNumber: big.NewInt(1),

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
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
	if err = os.WriteFile(keyFile, keyData, os.FileMode(0600)); err != nil {
		return nil, err
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

// // Generates a cert and key from the specified template, signing it with the specified parent cert and key.
// // If the parent cert or key are nil, makes the generated cert self-signed.
// func GenerateSignedCertAndKey(template *x509.Certificate, caCert *x509.Certificate, caKey *crypto.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
// 	key, err := GenerateKey()
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("generating private key: %v", err)
// 	}
// 	var certDER []byte
// 	if caCert == nil || caKey == nil {
// 		certDER, err = x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
// 	} else {
// 		certDER, err = x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, *caKey)
// 	}
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("generating signed cert: %v", err)
// 	}
// 	cert, err := x509.ParseCertificate(certDER)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("parsing cert: %v", err)
// 	}
// 	return cert, key, nil
// }

// // Generates a CA cert and key, signed by the specified parent cert and key (or self-signed if the parent cert or key are nil)
// func GenerateSignedCACertAndKey(cfg Config, caCert *x509.Certificate, caKey *crypto.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
// 	template := &x509.Certificate{
// 		SerialNumber: randomSerial(),
// 		Subject: pkix.Name{
// 			CommonName:   cfg.CommonName,
// 			Organization: cfg.Organization,
// 		},
// 		DNSNames:              []string{cfg.CommonName},
// 		NotBefore:             cfg.NotBefore,
// 		NotAfter:              cfg.NotAfter,
// 		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
// 		PublicKeyAlgorithm:    x509.ECDSA,
// 		SignatureAlgorithm:    x509.ECDSAWithSHA256,
// 		BasicConstraintsValid: true,
// 		IsCA:                  true,
// 	}
// 	return GenerateSignedCertAndKey(template, caCert, caKey)
// }

// // func (ca *CA) MakeServerCert(hostnames sets.String, expireDays int, fns ...CertificateExtensionFunc) (*TLSCertificateConfig, error) {
// // 	serverPublicKey, serverPrivateKey, publicKeyHash, _ := newKeyPairWithHash()
// // 	authorityKeyId := ca.Config.Certs[0].SubjectKeyId
// // 	subjectKeyId := publicKeyHash
// // 	serverTemplate := newServerCertificateTemplate(pkix.Name{CommonName: hostnames.List()[0]}, hostnames.List(), expireDays, time.Now, authorityKeyId, subjectKeyId)
// // 	for _, fn := range fns {
// // 		if err := fn(serverTemplate); err != nil {
// // 			return nil, err
// // 		}
// // 	}
// // 	serverCrt, err := ca.signCertificate(serverTemplate, serverPublicKey)
// // 	if err != nil {
// // 		return nil, err
// // 	}
// // 	server := &TLSCertificateConfig{
// // 		Certs: append([]*x509.Certificate{serverCrt}, ca.Config.Certs...),
// // 		Key:   serverPrivateKey,
// // 	}
// // 	return server, nil
// // }

// // Generates a server cert and key, signed by the specified parent cert and key
// func GenerateSignedServerCertAndKey(cfg Config, caCert *x509.Certificate, caKey *crypto.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
// 	template := &x509.Certificate{
// 		SerialNumber: randomSerial(),
// 		Subject: pkix.Name{
// 			CommonName:   cfg.CommonName,
// 			Organization: cfg.Organization,
// 		},
// 		DNSNames:              []string{cfg.CommonName},
// 		NotBefore:             cfg.NotBefore,
// 		NotAfter:              cfg.NotAfter,
// 		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
// 		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
// 		PublicKeyAlgorithm:    x509.ECDSA,
// 		SignatureAlgorithm:    x509.ECDSAWithSHA256,
// 		BasicConstraintsValid: true,
// 		IsCA:                  false,
// 	}
// 	return GenerateSignedCertAndKey(template, caCert, caKey)
// }

// // Generates a client cert and key, signed by the specified parent cert and key
// func GenerateSignedClientCertAndKey(cfg Config, caCert *x509.Certificate, caKey *crypto.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, error) {
// 	template := &x509.Certificate{
// 		SerialNumber: randomSerial(),
// 		Subject: pkix.Name{
// 			CommonName:   cfg.CommonName,
// 			Organization: cfg.Organization,
// 		},
// 		DNSNames:              []string{cfg.CommonName},
// 		NotBefore:             cfg.NotBefore,
// 		NotAfter:              cfg.NotAfter,
// 		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
// 		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
// 		PublicKeyAlgorithm:    x509.ECDSA,
// 		SignatureAlgorithm:    x509.ECDSAWithSHA256,
// 		BasicConstraintsValid: true,
// 		IsCA:                  false,
// 	}
// 	return GenerateSignedCertAndKey(template, caCert, caKey)
// }

// func GenerateCSR(cfg Config, key *crypto.Signer) (*x509.CertificateRequest, error) {
// 	template := &x509.CertificateRequest{
// 		Subject: pkix.Name{
// 			CommonName:   cfg.CommonName,
// 			Organization: cfg.Organization,
// 		},
// 	}
// 	csrDER, err := x509.CreateCertificateRequest(rand.Reader, template, key)
// 	if err != nil {
// 		return nil, fmt.Errorf("creating csr: %v", err)
// 	}
// 	return x509.ParseCertificateRequest(csrDER)
// }

// func LoadOrGenerateSignerCACertAndKey(certDir string) ([]*x509.Certificate, *crypto.PrivateKey, error) {
// 	caCert, caKey, err := LoadCertAndKey(certDir, "signer-ca")
// 	if err == nil {
// 		return caCert, caKey, nil
// 	}

// 	newCaCert, newCaKey, err := GenerateSignedCACertAndKey(Config{
// 		CommonName:   "signer-ca",
// 		Organization: []string{"signer-ca"},
// 		NotBefore:    time.Now().Add(clockSkewMargin),
// 		NotAfter:     time.Now().Add(caCertLifetime),
// 	}, nil, nil)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("generating self-signed CA cert: %v", err)
// 	}
// 	if err := WriteCert(filepath.Join(certDir, "signer-ca.crt"), newCaCert); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA cert: %v", err)
// 	}
// 	if err := WriteKey(filepath.Join(certDir, "signer-ca.key"), newCaKey); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA key: %v", err)
// 	}
// 	return LoadCertAndKey(certDir, "signer-ca")
// }

// func LoadOrGenerateServerCertAndKey(certDir string, caCert *x509.Certificate, caKey *crypto.PrivateKey) ([]*x509.Certificate, *crypto.PrivateKey, error) {
// 	cert, key, err := LoadCertAndKey(certDir, "server")
// 	if err == nil {
// 		return cert, key, nil
// 	}

// 	newCert, newKey, err := GenerateSignedServerCertAndKey(Config{
// 		CommonName:   "server",
// 		Organization: []string{"server"},
// 		NotBefore:    time.Now().Add(clockSkewMargin),
// 		NotAfter:     time.Now().Add(caCertLifetime),
// 	}, caCert, caKey)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("generating server cert: %v", err)
// 	}
// 	if err := WriteCert(filepath.Join(certDir, "server.crt"), newCert); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA cert: %v", err)
// 	}
// 	if err := WriteKey(filepath.Join(certDir, "server.key"), newKey); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA key: %v", err)
// 	}
// 	return LoadCertAndKey(certDir, "server")
// }

// func LoadOrGenerateClientCertAndKey(certDir string, name string, caCert *x509.Certificate, caKey *crypto.PrivateKey) ([]*x509.Certificate, *crypto.PrivateKey, error) {
// 	cert, key, err := LoadCertAndKey(certDir, name)
// 	if err == nil {
// 		return cert, key, nil
// 	}

// 	newCert, newKey, err := GenerateSignedServerCertAndKey(Config{
// 		CommonName:   name,
// 		Organization: []string{name},
// 		NotBefore:    time.Now().Add(clockSkewMargin),
// 		NotAfter:     time.Now().Add(caCertLifetime),
// 	}, caCert, caKey)
// 	if err != nil {
// 		return nil, nil, fmt.Errorf("generating server cert: %v", err)
// 	}
// 	if err := WriteCert(filepath.Join(certDir, name+".crt"), newCert); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA cert: %v", err)
// 	}
// 	if err := WriteKey(filepath.Join(certDir, name+".key"), newKey); err != nil {
// 		return nil, nil, fmt.Errorf("writing CA key: %v", err)
// 	}
// 	return LoadCertAndKey(certDir, name)
// }

// func LoadCertAndKey(certDir string, certName string) ([]*x509.Certificate, *crypto.PrivateKey, error) {
// 	certs, err := LoadCerts(filepath.Join(certDir, certName+".crt"))
// 	if err != nil {
// 		return nil, nil, err
// 	}
// 	key, err := LoadKey(filepath.Join(certDir, certName+".key"))
// 	if err != nil {
// 		return nil, nil, err
// 	}
// 	return certs, key, nil
// }

// func LoadCerts(file string) ([]*x509.Certificate, error) {
// 	pemBlock, err := os.ReadFile(file)
// 	if err != nil {
// 		return nil, err
// 	}
// 	certs, err := ParseCertsPEM(pemBlock)
// 	if err != nil {
// 		return nil, fmt.Errorf("error reading %s: %s", file, err)
// 	}
// 	return certs, nil
// }

// func LoadKey(file string) (*crypto.PrivateKey, error) {
// 	pemBlock, err := os.ReadFile(file)
// 	if err != nil {
// 		return nil, err
// 	}
// 	key, err := ParseKeyPEM(pemBlock)
// 	if err != nil {
// 		return nil, fmt.Errorf("error reading %s: %v", file, err)
// 	}
// 	return &key, nil
// }

// func WriteCert(certPath string, cert *x509.Certificate) error {
// 	certPEM := new(bytes.Buffer)
// 	if err := pem.Encode(certPEM, &pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}); err != nil {
// 		return fmt.Errorf("encoding certificate: %v", err)
// 	}
// 	if err := os.MkdirAll(filepath.Dir(certPath), os.FileMode(0755)); err != nil {
// 		return fmt.Errorf("creating directory for certificate: %v", err)
// 	}
// 	return os.WriteFile(certPath, certPEM.Bytes(), os.FileMode(0644))
// }

// func WriteKey(keyPath string, key any) error {
// 	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
// 	if err != nil {
// 		return fmt.Errorf("DER encoding private key: %v", err)
// 	}
// 	keyPEM := new(bytes.Buffer)
// 	if err := pem.Encode(keyPEM, &pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}); err != nil {
// 		return fmt.Errorf("PEM encoding private key: %v", err)
// 	}
// 	if err := os.MkdirAll(filepath.Dir(keyPath), os.FileMode(0755)); err != nil {
// 		return fmt.Errorf("creating directory for private key: %v", err)
// 	}
// 	return os.WriteFile(keyPath, keyPEM.Bytes(), os.FileMode(0600))
// }

// // ParseCertsPEM returns the x509.Certificates contained in the given PEM-encoded byte array
// // Returns an error if a certificate could not be parsed, or if the data does not contain any certificates
// func ParseCertsPEM(pemCerts []byte) ([]*x509.Certificate, error) {
// 	ok := false
// 	certs := []*x509.Certificate{}
// 	for len(pemCerts) > 0 {
// 		var block *pem.Block
// 		block, pemCerts = pem.Decode(pemCerts)
// 		if block == nil {
// 			break
// 		}
// 		// Only use PEM "CERTIFICATE" blocks without extra headers
// 		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
// 			continue
// 		}

// 		cert, err := x509.ParseCertificate(block.Bytes)
// 		if err != nil {
// 			return certs, err
// 		}

// 		certs = append(certs, cert)
// 		ok = true
// 	}

// 	if !ok {
// 		return certs, errors.New("data does not contain any valid RSA or ECDSA certificates")
// 	}
// 	return certs, nil
// }
