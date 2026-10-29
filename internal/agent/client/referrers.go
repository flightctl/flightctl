package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	dockerconfig "github.com/containers/image/v5/pkg/docker/config"
	"github.com/containers/image/v5/pkg/sysregistriesv2"
	"github.com/containers/image/v5/types"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

// ListRegistryReferrers uses ORAS's OCI Referrers API implementation, including
// pagination and the Referrers Tag Schema fallback. It runs in the storage
// owner's helper process so containers/image can use that user's registry
// configuration, credential helpers, and default authentication files.
func ListRegistryReferrers(ctx context.Context, rw fileio.ReadWriter, image string, opts ...ClientOption) (*OCIIndex, error) {
	options := &clientOptions{}
	for _, opt := range opts {
		opt(options)
	}
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return nil, fmt.Errorf("parse referrers subject: %w", err)
	}
	subject, ok := named.(reference.Digested)
	if !ok {
		return nil, fmt.Errorf("referrers subject must include a manifest digest")
	}
	if err := subject.Digest().Validate(); err != nil {
		return nil, fmt.Errorf("invalid referrers subject digest: %w", err)
	}
	sys := &types.SystemContext{}
	if root := rw.PathFor("/"); root != "/" {
		sys.RootForImplicitAbsolutePaths = root
		sys.SystemRegistriesConfPath = rw.PathFor("/etc/containers/registries.conf")
		sys.SystemRegistriesConfDirPath = rw.PathFor("/etc/containers/registries.conf.d")
	}
	if options.pullSecretPath != "" {
		sys.AuthFilePath = rw.PathFor(options.pullSecretPath)
	} else if !options.defaultAuth {
		sys.DockerAuthConfig = &types.DockerAuthConfig{}
	}
	registry, err := sysregistriesv2.FindRegistry(sys, named.Name())
	if err != nil {
		return nil, fmt.Errorf("load container registries configuration: %w", err)
	}
	sources := []sysregistriesv2.PullSource{{Reference: named}}
	if registry != nil {
		if registry.Blocked {
			return nil, fmt.Errorf("registry %s is blocked", registry.Prefix)
		}
		sources, err = registry.PullSourcesFromReference(named)
		if err != nil {
			return nil, fmt.Errorf("resolve registry endpoints: %w", err)
		}
	}
	var failures []error
	var empty *OCIIndex
	for _, source := range sources {
		index, err := registrySourceReferrers(ctx, rw, sys, source, subject.Digest())
		if err == nil {
			if len(index.Manifests) > 0 {
				return index, nil
			}
			empty = index
		} else {
			failures = append(failures, fmt.Errorf("referrers for %s: %w", source.Reference.Name(), err))
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if empty != nil {
		return empty, nil
	}
	return nil, fmt.Errorf("query registry referrers: %w", errors.Join(failures...))
}

func registrySourceReferrers(ctx context.Context, rw fileio.ReadWriter, sys *types.SystemContext, source sysregistriesv2.PullSource, subject digest.Digest) (*OCIIndex, error) {
	repo, err := remote.NewRepository(source.Reference.Name())
	if err != nil {
		return nil, err
	}
	credential, err := dockerconfig.GetCredentialsForRef(sys, source.Reference)
	if err != nil {
		return nil, fmt.Errorf("load registry credentials: %w", err)
	}
	tlsConfig, err := registryTLSConfig(rw, reference.Domain(source.Reference), source.Endpoint.Insecure)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = tlsConfig
	defer transport.CloseIdleConnections()
	repo.Client = &auth.Client{
		Client: &http.Client{Transport: transport},
		Cache:  auth.NewCache(),
		Credential: auth.StaticCredential(repo.Reference.Host(), auth.Credential{
			Username:     credential.Username,
			Password:     credential.Password,
			RefreshToken: credential.IdentityToken,
		}),
	}
	repo.ReferrerListMaxPages = 100
	index, err := fetchRegistryReferrers(ctx, repo, subject)
	// Match containers/image's insecure-registry behavior: allow plain HTTP
	// after a failed HTTPS connection, but never downgrade an HTTP API error.
	var connectionError *url.Error
	if source.Endpoint.Insecure && errors.As(err, &connectionError) && ctx.Err() == nil {
		repo.PlainHTTP = true
		index, err = fetchRegistryReferrers(ctx, repo, subject)
	}
	return index, err
}

func fetchRegistryReferrers(ctx context.Context, repo *remote.Repository, subject digest.Digest) (*OCIIndex, error) {
	index := &OCIIndex{SchemaVersion: 2, MediaType: ocispec.MediaTypeImageIndex, Manifests: []OCIReferrer{}}
	err := repo.Referrers(ctx, ocispec.Descriptor{Digest: subject}, "", func(refs []ocispec.Descriptor) error {
		for _, ref := range refs {
			index.Manifests = append(index.Manifests, OCIReferrer{
				MediaType: ref.MediaType, Digest: ref.Digest.String(), Size: ref.Size,
				ArtifactType: ref.ArtifactType, Annotations: ref.Annotations,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return index, nil
}

func registryTLSConfig(rw fileio.ReadWriter, host string, insecure bool) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure} //nolint:gosec // Explicit containers registries.conf policy.
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve registry certificate home: %w", err)
	}
	for _, base := range []string{filepath.Join(home, ".config/containers/certs.d"), "/etc/containers/certs.d", "/etc/docker/certs.d"} {
		dir := filepath.Join(base, host)
		entries, err := rw.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read registry certificate directory: %w", err)
		}
		if err := loadRegistryCertificates(rw, dir, entries, config); err != nil {
			return nil, err
		}
		break
	}
	return config, nil
}

func loadRegistryCertificates(rw fileio.ReadWriter, dir string, entries []fs.DirEntry, config *tls.Config) error {
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".crt") && !strings.HasSuffix(name, ".cert") {
			continue
		}
		data, err := rw.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("read registry certificate: %w", err)
		}
		if strings.HasSuffix(name, ".crt") {
			if config.RootCAs == nil {
				config.RootCAs, err = x509.SystemCertPool()
				if err != nil {
					return fmt.Errorf("load system certificate pool: %w", err)
				}
			}
			if !config.RootCAs.AppendCertsFromPEM(data) {
				return fmt.Errorf("invalid registry CA certificate %s", name)
			}
		} else {
			key, err := rw.ReadFile(filepath.Join(dir, strings.TrimSuffix(name, ".cert")+".key"))
			if err != nil {
				return fmt.Errorf("read registry client key: %w", err)
			}
			cert, err := tls.X509KeyPair(data, key)
			if err != nil {
				return fmt.Errorf("parse registry client certificate: %w", err)
			}
			config.Certificates = append(config.Certificates, cert)
		}
	}
	return nil
}
