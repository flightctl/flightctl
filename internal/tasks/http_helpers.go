package tasks

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

func buildHttpRepoRequestAuth(repoHttpSpec api.HttpRepoSpec, req *http.Request) (*http.Request, *tls.Config, error) {
	if repoHttpSpec.HttpConfig.Username != nil && repoHttpSpec.HttpConfig.Password != nil {
		req.SetBasicAuth(*repoHttpSpec.HttpConfig.Username, *repoHttpSpec.HttpConfig.Password)
	}
	if repoHttpSpec.HttpConfig.Token != nil {
		req.Header.Set("Authorization", "Bearer "+*repoHttpSpec.HttpConfig.Token)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if repoHttpSpec.HttpConfig.TlsCrt != nil && repoHttpSpec.HttpConfig.TlsKey != nil {
		cert, err := base64.StdEncoding.DecodeString(*repoHttpSpec.HttpConfig.TlsCrt)
		if err != nil {
			return nil, tlsConfig, err
		}

		key, err := base64.StdEncoding.DecodeString(*repoHttpSpec.HttpConfig.TlsKey)
		if err != nil {
			return nil, tlsConfig, err
		}

		tlsPair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, tlsConfig, err
		}

		tlsConfig.Certificates = []tls.Certificate{tlsPair}
	}

	if repoHttpSpec.HttpConfig.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*repoHttpSpec.HttpConfig.CaCrt)
		if err != nil {
			return nil, tlsConfig, err
		}

		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			return nil, tlsConfig, err
		}
		if rootCAs == nil {
			rootCAs = x509.NewCertPool()
		}
		rootCAs.AppendCertsFromPEM(ca)
		tlsConfig.RootCAs = rootCAs
	}
	if repoHttpSpec.HttpConfig.SkipServerVerification != nil {
		tlsConfig.InsecureSkipVerify = *repoHttpSpec.HttpConfig.SkipServerVerification
	}

	return req, tlsConfig, nil
}
