package crypto

import (
	"crypto/tls"
	"crypto/x509"

	oscrypto "github.com/openshift/library-go/pkg/crypto"
)

func (ca *CA) TLSConfigForServer(s *TLSCertificateConfig) (*tls.Config, error) {
	certBytes, err := oscrypto.EncodeCertificates(s.Certs...)
	if err != nil {
		return nil, err
	}
	keyBytes, err := PEMEncodeKey(s.Key)
	if err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair(certBytes, keyBytes)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	for _, caCert := range ca.Config.Certs {
		caPool.AddCert(caCert)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
