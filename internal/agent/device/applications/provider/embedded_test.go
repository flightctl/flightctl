package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// setupTestEnv creates a test environment with logger, podman mock, and readWriter
func setupTestEnv(t *testing.T) (*log.PrefixLogger, *client.Podman, fileio.ReadWriter) {
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	ctrl := gomock.NewController(t)
	t.Cleanup(func() { ctrl.Finish() })

	mockExec := executer.NewMockExecuter(ctrl)
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter()
	rw.SetRootdir(tmpDir)
	podman := client.NewPodman(log, mockExec, rw, util.NewPollConfig())

	// Create the systemd unit directory for target file copying
	require.NoError(t, rw.MkdirAll(lifecycle.QuadletTargetPath, fileio.DefaultDirectoryPermissions))

	return log, podman, rw
}

// setupEmbeddedQuadletApp creates an embedded quadlet app directory with specified files
func setupEmbeddedQuadletApp(t *testing.T, rw fileio.ReadWriter, name string, files map[string]string) {
	embeddedPath := filepath.Join(lifecycle.EmbeddedQuadletAppPath, name)
	require.NoError(t, rw.MkdirAll(embeddedPath, fileio.DefaultDirectoryPermissions))

	for filename, content := range files {
		filePath := filepath.Join(embeddedPath, filename)
		require.NoError(t, rw.WriteFile(filePath, []byte(content), fileio.DefaultFilePermissions))
	}
}

// setupRealQuadletApp creates a real path quadlet app for remove testing
func setupRealQuadletApp(t *testing.T, rw fileio.ReadWriter, name string, files map[string]string) {
	realPath := filepath.Join(lifecycle.QuadletAppPath, name)
	require.NoError(t, rw.MkdirAll(realPath, fileio.DefaultDirectoryPermissions))

	for filename, content := range files {
		filePath := filepath.Join(realPath, filename)
		require.NoError(t, rw.WriteFile(filePath, []byte(content), fileio.DefaultFilePermissions))
	}
}

// verifyFilesExist verifies files exist at given paths
func verifyFilesExist(t *testing.T, rw fileio.ReadWriter, paths []string) {
	for _, path := range paths {
		exists, err := rw.PathExists(path)
		require.NoError(t, err)
		require.True(t, exists, "expected file to exist: %s", path)
	}
}

// verifyFilesNotExist verifies files don't exist at given paths
func verifyFilesNotExist(t *testing.T, rw fileio.ReadWriter, paths []string) {
	for _, path := range paths {
		exists, err := rw.PathExists(path)
		if err != nil && !fileio.IsNotExist(err) {
			require.NoError(t, err)
		}
		require.False(t, exists, "expected file to not exist: %s", path)
	}
}

// verifyFileNamespaced verifies file contains namespaced references
func verifyFileNamespaced(t *testing.T, rw fileio.ReadWriter, path, appID string) {
	content, err := rw.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(content), appID, "expected file to contain appID: %s", path)
}

func TestEmbeddedProvider_Install(t *testing.T) {
	tests := []struct {
		name     string
		appName  string
		files    map[string]string
		verifyFn func(t *testing.T, rw fileio.ReadWriter, appName string)
	}{
		{
			name:    "quadlet with single container file",
			appName: "myapp",
			files: map[string]string{
				"web.container": `[Container]
Image=nginx:latest
`,
			},
			verifyFn: func(t *testing.T, rw fileio.ReadWriter, appName string) {
				realPath := filepath.Join(lifecycle.QuadletAppPath, appName)
				appID := client.NewComposeID(appName)

				// Verify namespaced file exists
				namespacedFile := filepath.Join(realPath, appID+"-web.container")
				verifyFilesExist(t, rw, []string{namespacedFile})

				// Verify drop-in directory and file exist
				dropInDir := filepath.Join(realPath, appID+"-.container.d")
				dropInFile := filepath.Join(dropInDir, "99-flightctl.conf")
				verifyFilesExist(t, rw, []string{dropInFile})

				// Verify drop-in file contains appID label
				verifyFileNamespaced(t, rw, dropInFile, appID)
			},
		},
		{
			name:    "quadlet with multiple file types",
			appName: "multiapp",
			files: map[string]string{
				"web.container": `[Container]
Image=nginx:latest
Volume=data.volume:/data
Network=app-net.network
`,
				"data.volume": `[Volume]
`,
				"app-net.network": `[Network]
`,
			},
			verifyFn: func(t *testing.T, rw fileio.ReadWriter, appName string) {
				realPath := filepath.Join(lifecycle.QuadletAppPath, appName)
				appID := client.NewComposeID(appName)

				// Verify all namespaced files exist
				namespacedContainer := filepath.Join(realPath, appID+"-web.container")
				namespacedVolume := filepath.Join(realPath, appID+"-data.volume")
				namespacedNetwork := filepath.Join(realPath, appID+"-app-net.network")
				verifyFilesExist(t, rw, []string{namespacedContainer, namespacedVolume, namespacedNetwork})

				// Verify drop-in files for each type exist
				containerDropIn := filepath.Join(realPath, appID+"-.container.d", "99-flightctl.conf")
				volumeDropIn := filepath.Join(realPath, appID+"-.volume.d", "99-flightctl.conf")
				networkDropIn := filepath.Join(realPath, appID+"-.network.d", "99-flightctl.conf")
				verifyFilesExist(t, rw, []string{containerDropIn, volumeDropIn, networkDropIn})

				// Verify container file has namespaced references
				containerContent, err := rw.ReadFile(namespacedContainer)
				require.NoError(t, err)
				require.Contains(t, string(containerContent), appID+"-data.volume")
				require.Contains(t, string(containerContent), appID+"-app-net.network")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, podman, rw := setupTestEnv(t)

			// Setup embedded app
			setupEmbeddedQuadletApp(t, rw, tt.appName, tt.files)

			// Create embedded provider
			provider, err := newEmbedded(logger, podman, rw, tt.appName, v1beta1.AppTypeQuadlet, "2025-01-01T00:00:00Z", false)
			require.NoError(t, err)

			// Call Install
			ctx := context.Background()
			err = provider.Install(ctx)
			require.NoError(t, err)

			// Verify results
			if tt.verifyFn != nil {
				tt.verifyFn(t, rw, tt.appName)
			}
		})
	}
}

func TestEmbeddedProvider_Remove(t *testing.T) {
	tests := []struct {
		name     string
		appName  string
		files    map[string]string
		verifyFn func(t *testing.T, rw fileio.ReadWriter, appName string)
	}{
		{
			name:    "remove quadlet app",
			appName: "myapp",
			files: map[string]string{
				"myapp-web.container":                  `[Container]`,
				"myapp-.container.d/99-flightctl.conf": `[Container]`,
			},
			verifyFn: func(t *testing.T, rw fileio.ReadWriter, appName string) {
				// Verify real path is removed
				realPath := filepath.Join(lifecycle.QuadletAppPath, appName)
				verifyFilesNotExist(t, rw, []string{realPath})

				// Verify embedded path still exists
				embeddedPath := filepath.Join(lifecycle.EmbeddedQuadletAppPath, appName)
				exists, err := rw.PathExists(embeddedPath)
				require.NoError(t, err)
				require.True(t, exists, "embedded path should still exist")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, podman, rw := setupTestEnv(t)

			// Setup embedded app (to ensure it exists)
			setupEmbeddedQuadletApp(t, rw, tt.appName, map[string]string{"web.container": "[Container]\n"})

			// Setup real app (to be removed)
			setupRealQuadletApp(t, rw, tt.appName, tt.files)

			// Create embedded provider
			provider, err := newEmbedded(logger, podman, rw, tt.appName, v1beta1.AppTypeQuadlet, "2025-01-01T00:00:00Z", false)
			require.NoError(t, err)

			// Call Remove
			ctx := context.Background()
			err = provider.Remove(ctx)
			require.NoError(t, err)

			// Verify results
			if tt.verifyFn != nil {
				tt.verifyFn(t, rw, tt.appName)
			}
		})
	}
}

func TestParseEmbeddedQuadlet(t *testing.T) {
	tests := []struct {
		name          string
		apps          map[string]map[string]string
		expectedCount int
		expectedNames []string
	}{
		{
			name: "single quadlet app with container file",
			apps: map[string]map[string]string{
				"myapp": {
					"web.container": "[Container]\nImage=nginx:latest\n",
				},
			},
			expectedCount: 1,
			expectedNames: []string{"myapp"},
		},
		{
			name: "multiple quadlet apps discovered",
			apps: map[string]map[string]string{
				"app1": {
					"web.container": "[Container]\nImage=nginx:latest\n",
				},
				"app2": {
					"db.container": "[Container]\nImage=postgres:latest\n",
				},
				"app3": {
					"cache.container": "[Container]\nImage=redis:latest\n",
				},
			},
			expectedCount: 3,
			expectedNames: []string{"app1", "app2", "app3"},
		},
		{
			name: "different supported extensions",
			apps: map[string]map[string]string{
				"container-app": {
					"web.container": "[Container]\n",
				},
				"volume-app": {
					"data.volume": "[Volume]\n",
				},
				"network-app": {
					"net.network": "[Network]\n",
				},
				"pod-app": {
					"mypod.pod": "[Pod]\n",
				},
				"image-app": {
					"myimg.image": "[Image]\n",
				},
			},
			expectedCount: 5,
			expectedNames: []string{"container-app", "volume-app", "network-app", "pod-app", "image-app"},
		},
		{
			name: "multiple supported files in one app directory",
			apps: map[string]map[string]string{
				"fullapp": {
					"web.container":   "[Container]\n",
					"data.volume":     "[Volume]\n",
					"app-net.network": "[Network]\n",
				},
			},
			expectedCount: 1,
			expectedNames: []string{"fullapp"},
		},
		{
			name: "unsupported extensions ignored",
			apps: map[string]map[string]string{
				"build-app": {
					"app.build": "[Build]\n",
				},
				"kube-app": {
					"app.kube": "[Kube]\n",
				},
			},
			expectedCount: 0,
			expectedNames: []string{},
		},
		{
			name:          "empty embedded quadlet directory",
			apps:          map[string]map[string]string{},
			expectedCount: 0,
			expectedNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, podman, rw := setupTestEnv(t)

			// Setup all apps
			for appName, files := range tt.apps {
				setupEmbeddedQuadletApp(t, rw, appName, files)
			}

			// Call parseEmbeddedQuadlet
			var providers []Provider
			ctx := context.Background()
			err := parseEmbeddedQuadlet(ctx, logger, podman, rw, &providers, "2025-01-01T00:00:00Z")
			require.NoError(t, err)

			// Verify count
			require.Equal(t, tt.expectedCount, len(providers), "unexpected number of providers")

			// Verify provider names and types
			providerNames := make(map[string]bool)
			for _, p := range providers {
				providerNames[p.Name()] = true
				require.Equal(t, v1beta1.AppTypeQuadlet, p.Spec().AppType, "expected AppTypeQuadlet")
			}

			for _, expectedName := range tt.expectedNames {
				require.True(t, providerNames[expectedName], "expected provider not found: %s", expectedName)
			}
		})
	}
}
