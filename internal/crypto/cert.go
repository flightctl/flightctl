package crypto

import (
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/config"
	oscrypto "github.com/openshift/library-go/pkg/crypto"
	"k8s.io/apimachinery/pkg/util/sets"
)

// Wraps openshift/library-go/pkg/crypto to use ECDSA and simplify the interface
const ClientBootstrapCommonName = "client-enrollment"
const ClientBootstrapCommonNamePrefix = "client-enrollment-"
const AdminCommonName = "flightctl-admin"
const DeviceCommonNamePrefix = "device:"

const (
	CaCertValidityDays          = 365 * 10
	ServerCertValidityDays      = 365 * 1
	ClientBootStrapValidityDays = 365 * 1
	SignerCertName              = "ca"
	ServerCertName              = "server"
	ClientBootstrapCertName     = "client-enrollment"
)

func BootstrapCNFromName(name string) (string, error) {
	if len(name) < 16 {
		return "", flterrors.ErrCNLength
	}
	return ClientBootstrapCommonNamePrefix + name, nil
}

func CNFromDeviceFingerprint(fingerprint string) (string, error) {
	if len(fingerprint) < 16 {
		return "", errors.New("device fingerprint must have 16 characters at least")
	}
	return DeviceCommonNamePrefix + fingerprint, nil
}

func CertFile(name string) string {
	return filepath.Join(config.CertificateDir(), name+".crt")
}

func KeyFile(name string) string {
	return filepath.Join(config.CertificateDir(), name+".key")
}

type TLSCertificateConfig oscrypto.TLSCertificateConfig

type CA interface {
	// Return TLSCertificateConfig which uses this CA as root for authentication
	GetConfig() *TLSCertificateConfig
	// Load server certificate and key given by filename if present. If that fails genereate one if supported by CA
	EnsureServerCertificate(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, bool, error)
	// Create a server cert, sign it using this CA and write it out to file
	MakeAndWriteServerCert(certFile, keyFile string, hostnames []string, expireDays int) (*TLSCertificateConfig, error)
	// Create a server cert, sign it using this CA (in memory only)
	MakeServerCert(hostnames []string, expiryDays int, fns ...CertificateExtensionFunc) (*TLSCertificateConfig, error)
	// Sign supplied certificate
	signCertificate(template *x509.Certificate, requestKey crypto.PublicKey) (*x509.Certificate, error)
	// Load client certificate from the supplied location. If that fails - generate it.
	EnsureClientCertificate(certFile, keyFile string, subjectName string, expireDays int) (*TLSCertificateConfig, bool, error)
	// Create a client (cli) cert, sign it using this CA and write it out to file
	MakeClientCertificate(certFile, keyFile string, subject string, expiryDays int) (*TLSCertificateConfig, error)
	// Sign supplied CSR and issue a certificate
	IssueRequestedClientCertificate(csr *x509.CertificateRequest, expirySeconds int) ([]byte, error)
}



func EnsureCA(cfg *config.CryptographyConfig) (CA, bool, error) {
	if cfg == nil || cfg.CA == nil {
		cfg = &config.CryptographyConfig{
			CA: &config.CryptographyConfigEntry{
					CAType:config.InternalCA,
					InternalCAcfg: &config.InternalCAConfig{
						Cert: CertFile(SignerCertName),
						Key: KeyFile(SignerCertName),
						Serial: "",
						SignerName: SignerCertName,
						ExpireDays: CaCertValidityDays,
					},
				},
			}
	}
	return configureCA(cfg.CA)
}

func configureCA(configData *config.CryptographyConfigEntry) (CA, bool, error) {
	switch configData.CAType {
		case config.InternalCA: {
			if ca, err := GetCA(configData.InternalCAcfg.Cert, configData.InternalCAcfg.Key, configData.InternalCAcfg.Serial); err == nil {
				return ca, false, err
			}
			ca, err := MakeSelfSignedCA(configData.InternalCAcfg.Cert, configData.InternalCAcfg.Key, configData.InternalCAcfg.Serial, configData.InternalCAcfg.SignerName, int(configData.InternalCAcfg.ExpireDays))
			return ca, true, err
		}
		default:
			return nil, false, fmt.Errorf("unsupported CA type: %d", configData.CAType)
	}
}

func GetCA(certFile, keyFile, serialFile string) (CA, error) {
	var loaded CA

	ca, err := oscrypto.GetCA(certFile, keyFile, serialFile)
	if err != nil {
		return nil, err
	}
	config := TLSCertificateConfig(*ca.Config)
	loaded = &internalCA{Config: &config, SerialGenerator: ca.SerialGenerator,}
	return loaded, err
}

func MakeSelfSignedCA(certFile, keyFile, serialFile, subjectName string, expiryDays int) (CA, error) {
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
	ca := &internalCA{
		SerialGenerator: serialGenerator,
		Config:          &config,
	}
	return ca, nil
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

func GetServerCert(certFile, keyFile string, hostnames []string) (*TLSCertificateConfig, error) {
	internalServer, err := oscrypto.GetServerCert(certFile, keyFile, sets.NewString(hostnames...))
	if err != nil {
		return nil, err
	}
	server := TLSCertificateConfig(*internalServer)
	return &server, nil
}

type CertificateExtensionFunc func(*x509.Certificate) error


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
