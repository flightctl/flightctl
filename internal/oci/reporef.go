package oci

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// BuildOciRepoRef creates a fully configured ORAS remote.Repository from an OciRepoSpec
// and a full reference string (e.g. "registry.example.com/my-image").
// It sets up TLS, credentials, and HTTP scheme based on the spec.
// Callers that push artifacts should also set repoRef.SkipReferrersGC = true.
func BuildOciRepoRef(ociSpec *domain.OciRepoSpec, ref string) (*remote.Repository, error) {
	repoRef, err := remote.NewRepository(ref)
	if err != nil {
		return nil, fmt.Errorf("create repository reference: %w", err)
	}

	authClient, err := newOCIAuthClient(ociSpec)
	if err != nil {
		return nil, err
	}
	repoRef.Client = authClient

	if ociSpec.Scheme != nil && *ociSpec.Scheme == domain.OciRepoSchemeHttp {
		repoRef.PlainHTTP = true
	}

	return repoRef, nil
}

// BuildOciTLSConfig builds a *tls.Config from an OciRepoSpec, applying InsecureSkipVerify
// and custom CA certificates as configured.
func BuildOciTLSConfig(ociSpec *domain.OciRepoSpec) (*tls.Config, error) {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if ociSpec.SkipServerVerification != nil && *ociSpec.SkipServerVerification {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec
	}

	if ociSpec.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*ociSpec.CaCrt)
		if err != nil {
			return nil, fmt.Errorf("decode CA certificate: %w", err)
		}
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}
		if !rootCAs.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("failed to append CA certificates from PEM")
		}
		tlsConfig.RootCAs = rootCAs
	}

	return tlsConfig, nil
}

// newOCIAuthClient builds an auth.Client from an OciRepoSpec.
// A custom HTTP transport is only attached when TLS settings are non-default,
// so callers that do not configure TLS options get the default transport.
func newOCIAuthClient(ociSpec *domain.OciRepoSpec) (*auth.Client, error) {
	authClient := &auth.Client{}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	customTLS := false

	if ociSpec.SkipServerVerification != nil && *ociSpec.SkipServerVerification {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec
		customTLS = true
	}

	if ociSpec.CaCrt != nil {
		ca, err := base64.StdEncoding.DecodeString(*ociSpec.CaCrt)
		if err != nil {
			return nil, fmt.Errorf("decode CA certificate: %w", err)
		}
		rootCAs, err := x509.SystemCertPool()
		if err != nil {
			rootCAs = x509.NewCertPool()
		}
		if !rootCAs.AppendCertsFromPEM(ca) {
			return nil, fmt.Errorf("failed to append CA certificates from PEM")
		}
		tlsConfig.RootCAs = rootCAs
		customTLS = true
	}

	if customTLS {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = tlsConfig
		authClient.Client = &http.Client{Transport: transport}
	}

	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err != nil {
			return nil, fmt.Errorf("failed to parse OCI authentication: %w", err)
		}
		if dockerAuth.Username != "" && dockerAuth.Password != "" {
			authClient.Credential = auth.StaticCredential(ociSpec.Registry, auth.Credential{
				Username: dockerAuth.Username,
				Password: dockerAuth.Password,
			})
		}
	}

	return authClient, nil
}
