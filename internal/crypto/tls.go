package crypto

import (
	"crypto/tls"
	"crypto/x509"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

func TLSConfigForServer(caBundlex509 []*x509.Certificate, serverConfig *TLSCertificateConfig) (*tls.Config, *tls.Config, error) {

	certs := append(serverConfig.Certs, caBundlex509...)

	certBytes, err := oscrypto.EncodeCertificates(certs...)
	if err != nil {
		return nil, nil, err
	}


	keyBytes, err := PEMEncodeKey(serverConfig.Key)
	if err != nil {
		return nil, nil, err
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, nil, err
	}

	caPool := x509.NewCertPool()
	for _, caCert := range caBundlex509 {
		caPool.AddCert(caCert)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	agentTlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}

	return tlsConfig, agentTlsConfig, nil
}

func TLSConfigForClient(caBundleX509 []*x509.Certificate, clientConfig *TLSCertificateConfig) (*tls.Config, error) {
	caPool := x509.NewCertPool()
	for _, caCert := range caBundleX509 {
		caPool.AddCert(caCert)
	}
	tlsConfig := &tls.Config{
		RootCAs:    caPool,
		MinVersion: tls.VersionTLS13,
	}

	if clientConfig != nil {
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
		tlsConfig.Certificates = []tls.Certificate{cert}
	}
	return tlsConfig, nil
}
