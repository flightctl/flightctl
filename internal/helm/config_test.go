package helm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/ignition"
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

func TestInspectionRepositoryAliases(t *testing.T) {
	for _, tt := range []struct {
		name, suffix string
	}{
		{name: "When a repository has no query it should supply an inspection alias"},
		{name: "When a repository has a query it should reject the inspection alias", suffix: "?channel=stable"},
		{name: "When a repository has an empty query it should reject the inspection alias", suffix: "?"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			const repoURL = "https://charts.example/charts"
			yaml := []byte("repositories:\n- name: child\n  url: " + repoURL + tt.suffix + "\n")
			wrapper, err := ignition.NewWrapper()
			require.NoError(t, err)
			wrapper.SetFile(helmRepositoryConfig, yaml, 0o644, false, "", "")
			registryConfig := []byte(`{"auths":{}}`)
			wrapper.SetFile(helmRegistryConfig, []byte(base64.StdEncoding.EncodeToString(registryConfig)), 0o600, true, "", "")
			config, err := InspectionConfig(wrapper.AsIgnitionConfig().Storage.Files)
			require.NoError(t, err)
			aliases, err := TargetRepositoryAliases(config)
			if tt.suffix == "" {
				require.NoError(t, err)
				require.Equal(t, []RepositoryAlias{{Name: "child", URL: repoURL}}, aliases)
			} else {
				require.Error(t, err)
				require.NotContains(t, err.Error(), repoURL)
				require.Empty(t, aliases)
			}
			home := t.TempDir()
			authPath, reposPath, _, err := prepareHelmConfig(t.Context(), home, home, config, nil, "oci://registry.example/team/chart:1.0")
			require.NoError(t, err)
			contents, err := os.ReadFile(reposPath)
			require.NoError(t, err)
			require.Equal(t, yaml, contents, "Helm acquisition uses the original repository URL")
			contents, err = os.ReadFile(authPath)
			require.NoError(t, err)
			require.Equal(t, registryConfig, contents)
		})
	}
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
