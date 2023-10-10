package crypto

import (
	"crypto/tls"
	"crypto/x509"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

func TLSConfigForServer(caConfig, serverConfig *TLSCertificateConfig) (*tls.Config, error) {
	certBytes, err := oscrypto.EncodeCertificates(serverConfig.Certs...)
	if err != nil {
		return nil, err
	}
	keyBytes, err := PEMEncodeKey(serverConfig.Key)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	for _, caCert := range caConfig.Certs {
		caPool.AddCert(caCert)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func TLSConfigForClient(caConfig, clientConfig *TLSCertificateConfig) (*tls.Config, error) {
	certBytes, err := oscrypto.EncodeCertificates(clientConfig.Certs...)
	if err != nil {
		return nil, err
	}
	keyBytes, err := PEMEncodeKey(clientConfig.Key)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	for _, caCert := range caConfig.Certs {
		caPool.AddCert(caCert)
	}
	return &tls.Config{
		RootCAs:      caPool,
		Certificates: []tls.Certificate{cert},
	}, nil
}
