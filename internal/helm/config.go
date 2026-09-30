package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
)

func prepareHelmConfig(
	ctx context.Context,
	workDir string,
	configHome string,
	renderedConfig []byte,
	registrySpec *domain.OciRepoSpec,
	chartRef string,
) (registryConfigPath, repositoryConfigPath string, transportArgs []string, err error) {
	files, err := renderedConfigFiles(renderedConfig)
	if err != nil {
		return "", "", nil, err
	}
	registryConfigPath, err = prepareHelmRegistryConfig(ctx, configHome, files, registrySpec)
	if err != nil {
		return "", "", nil, err
	}
	repositoryConfigPath, err = prepareHelmRepositoryConfig(configHome, files)
	if err != nil {
		return "", "", nil, err
	}
	transportArgs, err = prepareHelmTransportArgs(workDir, files, registrySpec, chartRef)
	if err != nil {
		return "", "", nil, err
	}
	return registryConfigPath, repositoryConfigPath, transportArgs, nil
}

func prepareHelmRegistryConfig(ctx context.Context, configHome string, files []domain.FileSpec, registrySpec *domain.OciRepoSpec) (string, error) {
	registryConfig, registryConfigFound, err := firstConfigFile(files, helmRegistryConfig, containerAuthConfig)
	if err != nil {
		return "", fmt.Errorf("read rendered Helm registry configuration: %w", err)
	}
	if !registryConfigFound {
		registryConfig, err = authConfigFromRepo(ctx, registrySpec)
		if err != nil {
			return "", err
		}
		registryConfigFound = len(registryConfig) > 0
	}
	if registryConfigFound {
		path := filepath.Join(configHome, "registry", "config.json")
		if err := writeHelmConfig(path, registryConfig); err != nil {
			return "", fmt.Errorf("write temporary Helm registry configuration: %w", err)
		}
		return path, nil
	}
	// Do not reuse the worker process's registry credentials. Device-rendered
	// config and organization-scoped Repository credentials are the only
	// credentials permitted for a device's Helm render.
	return "", nil
}

func prepareHelmRepositoryConfig(configHome string, files []domain.FileSpec) (string, error) {
	repositoryConfig, repositoryConfigFound, err := firstConfigFile(files, helmRepositoryConfig)
	if err != nil {
		return "", fmt.Errorf("read rendered Helm repository configuration: %w", err)
	}
	if repositoryConfigFound {
		path := filepath.Join(configHome, "repositories.yaml")
		if err := writeHelmConfig(path, repositoryConfig); err != nil {
			return "", fmt.Errorf("write temporary Helm repository configuration: %w", err)
		}
		return path, nil
	}
	// HELM_CONFIG_HOME is isolated for each render. Leave its repository config
	// empty rather than copying repositories or credentials from the worker.
	return "", nil
}

func prepareHelmTransportArgs(workDir string, files []domain.FileSpec, registrySpec *domain.OciRepoSpec, chartRef string) ([]string, error) {
	host, err := chartRegistryHost(chartRef)
	if err != nil {
		return nil, err
	}
	var transportArgs []string
	if registrySpec != nil && strings.EqualFold(registrySpec.Registry, host) {
		if registrySpec.Scheme != nil && *registrySpec.Scheme == domain.OciRepoSchemeHttp {
			transportArgs = append(transportArgs, "--plain-http")
		}
		if registrySpec.SkipServerVerification != nil && *registrySpec.SkipServerVerification {
			transportArgs = append(transportArgs, "--insecure-skip-tls-verify")
			return transportArgs, nil
		}
		if registrySpec.CaCrt != nil {
			ca, err := base64.StdEncoding.DecodeString(*registrySpec.CaCrt)
			if err != nil {
				return nil, fmt.Errorf("decode chart registry CA certificate: %w", err)
			}
			caPath, err := writeHelmCA(workDir, ca)
			if err != nil {
				return nil, err
			}
			transportArgs = append(transportArgs, "--ca-file", caPath)
			return transportArgs, nil
		}
	}

	// Registry CA files are also commonly installed through config providers or
	// mounted into the worker container. Honor those files when the Repository
	// resource does not carry an explicit CA.
	if !hasTransportArg(transportArgs, "--insecure-skip-tls-verify") && !hasTransportArg(transportArgs, "--ca-file") {
		caPath := filepath.Join(registryCertsDir, host, "ca.crt")
		ca, found, err := configFile(files, caPath)
		if err != nil {
			return nil, fmt.Errorf("read rendered chart registry CA certificate: %w", err)
		}
		if !found {
			ca, err = os.ReadFile(caPath)
			if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("read chart registry CA certificate %s: %w", caPath, err)
			}
			found = err == nil
		}
		if found {
			tempCAPath, err := writeHelmCA(workDir, ca)
			if err != nil {
				return nil, err
			}
			transportArgs = append(transportArgs, "--ca-file", tempCAPath)
		}
	}
	return transportArgs, nil
}

func writeHelmCA(workDir string, ca []byte) (string, error) {
	caPath := filepath.Join(workDir, "registry-ca.crt")
	if err := writeHelmConfig(caPath, ca); err != nil {
		return "", fmt.Errorf("write chart registry CA certificate: %w", err)
	}
	return caPath, nil
}

func renderedConfigFiles(renderedConfig []byte) ([]domain.FileSpec, error) {
	if len(renderedConfig) == 0 {
		return nil, nil
	}
	var files []domain.FileSpec
	if err := json.Unmarshal(renderedConfig, &files); err != nil {
		return nil, fmt.Errorf("decode rendered config files: %w", err)
	}
	return files, nil
}

func firstConfigFile(files []domain.FileSpec, paths ...string) ([]byte, bool, error) {
	for _, path := range paths {
		contents, found, err := configFile(files, path)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}
		if found {
			return contents, true, nil
		}
	}
	return nil, false, nil
}

func configFile(files []domain.FileSpec, path string) ([]byte, bool, error) {
	for _, file := range files {
		if file.Path != path {
			continue
		}
		contents, err := file.ContentsDecoded()
		if err != nil {
			return nil, true, err
		}
		return contents, true, nil
	}
	return nil, false, nil
}

func writeHelmConfig(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

func hasTransportArg(args []string, name string) bool {
	for _, arg := range args {
		if arg == name {
			return true
		}
	}
	return false
}
