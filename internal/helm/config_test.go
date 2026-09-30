package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPrepareHelmConfig(t *testing.T) {
	workDir := t.TempDir()
	configHome := filepath.Join(workDir, "config")
	registryAuth := []byte(`{"auths":{"registry.example":{"auth":"abc"}}}`)
	registryAuthB64 := base64.StdEncoding.EncodeToString(registryAuth)
	encoding := domain.EncodingBase64
	chartCA := []byte("test chart CA")
	chartCAB64 := base64.StdEncoding.EncodeToString(chartCA)
	config, err := json.Marshal([]domain.FileSpec{
		{Path: helmRegistryConfig, Content: registryAuthB64, ContentEncoding: &encoding},
		{Path: helmRepositoryConfig, Content: "repositories: []\n"},
		{Path: filepath.Join(registryCertsDir, "registry.example", "ca.crt"), Content: chartCAB64, ContentEncoding: &encoding},
	})
	require.NoError(t, err)

	registryConfigPath, repositoryConfigPath, transportArgs, err := prepareHelmConfig(
		context.Background(),
		workDir,
		configHome,
		config,
		&domain.OciRepoSpec{Registry: "registry.example"},
		"oci://registry.example/team/chart:1.0",
	)

	require.NoError(t, err)
	gotAuth, err := os.ReadFile(registryConfigPath)
	require.NoError(t, err)
	require.Equal(t, registryAuth, gotAuth)
	registryInfo, err := os.Stat(registryConfigPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), registryInfo.Mode().Perm())
	gotRepositories, err := os.ReadFile(repositoryConfigPath)
	require.NoError(t, err)
	require.Equal(t, "repositories: []\n", string(gotRepositories))
	require.Contains(t, transportArgs, "--ca-file")
	var caPath string
	for i, arg := range transportArgs {
		if arg == "--ca-file" && i+1 < len(transportArgs) {
			caPath = transportArgs[i+1]
			break
		}
	}
	require.NotEmpty(t, caPath)
	gotCA, err := os.ReadFile(caPath)
	require.NoError(t, err)
	require.Equal(t, chartCA, gotCA)
}

func TestPrepareHelmConfigDoesNotUseWorkerCredentials(t *testing.T) {
	workerConfigHome := t.TempDir()
	workerRegistryConfig := filepath.Join(workerConfigHome, "registry", "config.json")
	workerRepositoryConfig := filepath.Join(workerConfigHome, "repositories.yaml")
	require.NoError(t, os.MkdirAll(filepath.Dir(workerRegistryConfig), 0o700))
	require.NoError(t, os.WriteFile(workerRegistryConfig, []byte(`{"auths":{"private.example":{"auth":"worker-secret"}}}`), 0o600))
	require.NoError(t, os.WriteFile(workerRepositoryConfig, []byte("repositories:\n- name: private\n  url: https://private.example\n  username: worker\n  password: worker-secret\n"), 0o600))
	t.Setenv("HELM_REGISTRY_CONFIG", workerRegistryConfig)
	t.Setenv("HELM_REPOSITORY_CONFIG", workerRepositoryConfig)

	workDir := t.TempDir()
	configHome := filepath.Join(workDir, "isolated-config")
	registryConfigPath, repositoryConfigPath, _, err := prepareHelmConfig(
		context.Background(),
		workDir,
		configHome,
		nil,
		nil,
		"oci://registry.example/team/chart:1.0",
	)

	require.NoError(t, err)
	require.Empty(t, registryConfigPath)
	require.Empty(t, repositoryConfigPath)
	require.NoDirExists(t, filepath.Join(configHome, "registry"))
	require.NoFileExists(t, filepath.Join(configHome, "repositories.yaml"))
}
