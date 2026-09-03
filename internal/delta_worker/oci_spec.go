package delta_worker

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/oci"
)

func specForRegistry(host string, spec *domain.OciRepoSpec) *domain.OciRepoSpec {
	return oci.SpecForRegistry(host, spec)
}

func tlsSummary(spec *domain.OciRepoSpec) string {
	if spec == nil || spec.Registry == "" {
		return "default"
	}
	skip := spec.SkipServerVerification != nil && *spec.SkipServerVerification
	scheme := "https"
	if spec.Scheme != nil && *spec.Scheme != "" {
		scheme = string(*spec.Scheme)
	}
	return fmt.Sprintf("registry=%s scheme=%s skipTLS=%t ca=%t auth=%t", spec.Registry, scheme, skip, spec.CaCrt != nil, spec.OciAuth != nil)
}

func existenceConfigFromSpec(ctx context.Context, spec *domain.OciRepoSpec, imageRepository string) (existenceConfig, error) {
	out := existenceConfig{Scheme: "https", Client: &http.Client{Timeout: 30 * time.Second}}
	rewritten, err := oci.RewriteImageRef(imageRepository)
	if err != nil {
		return existenceConfig{}, err
	}
	host, _, err := splitRegistryRepository(rewritten)
	if err != nil {
		return out, nil
	}
	effective := specForRegistry(host, spec)
	if effective.Scheme != nil && *effective.Scheme != "" {
		out.Scheme = string(*effective.Scheme)
	}
	client, err := httpClientForSpec(effective)
	if err != nil {
		return existenceConfig{}, err
	}
	out.Client = client
	user, pass, err := credentialsFromSpec(ctx, effective)
	if err != nil {
		return existenceConfig{}, err
	}
	out.Username = user
	out.Password = pass
	return out, nil
}

func httpClientForSpec(spec *domain.OciRepoSpec) (*http.Client, error) {
	skip := spec.SkipServerVerification != nil && *spec.SkipServerVerification
	if !skip && spec.CaCrt == nil {
		return &http.Client{Timeout: 30 * time.Second}, nil
	}
	tlsConfig, err := oci.BuildOciTLSConfig(spec)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	return &http.Client{Transport: transport, Timeout: 30 * time.Second}, nil
}

func credentialsFromSpec(ctx context.Context, spec *domain.OciRepoSpec) (string, string, error) {
	if spec.OciAuth == nil {
		return "", "", nil
	}
	dockerAuth, err := spec.OciAuth.AsDockerAuth()
	if err != nil {
		return "", "", fmt.Errorf("parse OCI authentication: %w", err)
	}
	if dockerAuth.Username == "" || dockerAuth.Password == "" {
		return "", "", nil
	}
	decryptedPassword, _, err := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
	if err != nil {
		return "", "", fmt.Errorf("decrypt OCI password: %w", err)
	}
	return dockerAuth.Username, string(decryptedPassword), nil
}
