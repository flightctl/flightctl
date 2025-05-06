package util

import (
	"crypto/tls"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/client"
	certutil "k8s.io/client-go/util/cert"
)

func CompareDeviceVersion(last, current string) bool {
	lastInt, err := strconv.ParseInt(last, 10, 64)
	if err != nil {
		return false
	}

	currentInt, err := strconv.ParseInt(current, 10, 64)
	if err != nil {
		return false
	}

	return currentInt > lastInt
}

func GRPCConfig(config *client.Config) (string, *tls.Config, error) {
	config = config.DeepCopy()
	if err := config.Flatten(); err != nil {
		return "", nil, err
	}

	grpcEndpoint := config.Service.Server
	u, err := url.Parse(grpcEndpoint)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse grpc endpoint %s: %w", grpcEndpoint, err)
	}

	// our transport is http, but the grpc library has special encoding for the endpoint
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "http://")
	grpcEndpoint = strings.TrimPrefix(grpcEndpoint, "https://")
	grpcEndpoint = strings.TrimSuffix(grpcEndpoint, "/")

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: config.Service.InsecureSkipVerify, //nolint:gosec
	}

	if string(config.Service.CertificateAuthorityData) != "" {
		caPool, err := certutil.NewPoolFromBytes(config.Service.CertificateAuthorityData)
		if err != nil {
			return "", nil, fmt.Errorf("failed to parse CA certs: %w", err)
		}
		tlsConfig.RootCAs = caPool
	}

	tlsConfig.ServerName = u.Hostname()

	if len(config.AuthInfo.ClientCertificateData) > 0 {
		clientCert, err := tls.X509KeyPair(config.AuthInfo.ClientCertificateData, config.AuthInfo.ClientKeyData)
		if err != nil {
			return "", nil, fmt.Errorf("failed to parse client cert and key: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{clientCert}
	}

	return grpcEndpoint, tlsConfig, nil
}
