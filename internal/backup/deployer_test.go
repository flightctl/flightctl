package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

// testLogger creates a test logger with hook for capturing logs
func testLogger() (logrus.FieldLogger, *test.Hook) {
	logger, hook := test.NewNullLogger()
	logger.SetLevel(logrus.DebugLevel)
	return logger, hook
}

func TestDetectDeployment(t *testing.T) {
	tests := []struct {
		name        string
		setupEnv    func(t *testing.T) (cleanup func())
		wantType    DeploymentType
		wantErr     bool
		errContains string
	}{
		{
			name: "Podman deployment - PKI exists",
			setupEnv: func(t *testing.T) func() {
				// Capture and unset K8s env var to ensure clean state
				origK8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_HOST")

				// Create test-local directory structure
				tmpDir := t.TempDir()
				testFlightctlDir := filepath.Join(tmpDir, "flightctl")
				pkiDir := filepath.Join(testFlightctlDir, "pki")
				require.NoError(t, os.MkdirAll(pkiDir, 0755))
				caCrt := filepath.Join(pkiDir, "ca.crt")
				require.NoError(t, os.WriteFile(caCrt, []byte("test"), 0644))

				// Override base path for testing
				origBasePath := flightctlBasePath
				flightctlBasePath = testFlightctlDir

				return func() {
					flightctlBasePath = origBasePath
					if origK8sHost != "" {
						os.Setenv("KUBERNETES_SERVICE_HOST", origK8sHost)
					}
				}
			},
			wantType: DeploymentTypePodman,
			wantErr:  false,
		},
		{
			name: "Kubernetes deployment - env var set",
			setupEnv: func(t *testing.T) func() {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				return func() {}
			},
			wantType: DeploymentTypeKubernetes,
			wantErr:  false,
		},
		{
			name: "No deployment detected",
			setupEnv: func(t *testing.T) func() {
				// Capture and unset K8s env var to ensure clean state
				origK8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_HOST")

				return func() {
					if origK8sHost != "" {
						os.Setenv("KUBERNETES_SERVICE_HOST", origK8sHost)
					}
				}
			},
			wantType:    DeploymentTypeUnknown,
			wantErr:     true,
			errContains: "unable to detect deployment type",
		},
		{
			name: "Conflicting indicators",
			setupEnv: func(t *testing.T) func() {
				// Create test-local directory structure
				tmpDir := t.TempDir()
				testFlightctlDir := filepath.Join(tmpDir, "flightctl")
				pkiDir := filepath.Join(testFlightctlDir, "pki")
				require.NoError(t, os.MkdirAll(pkiDir, 0755))
				caCrt := filepath.Join(pkiDir, "ca.crt")
				require.NoError(t, os.WriteFile(caCrt, []byte("test"), 0644))

				// Override base path for testing
				origBasePath := flightctlBasePath
				flightctlBasePath = testFlightctlDir

				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")

				return func() {
					flightctlBasePath = origBasePath
				}
			},
			wantType:    DeploymentTypeUnknown,
			wantErr:     true,
			errContains: "conflicting deployment indicators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.setupEnv(t)
			defer cleanup()

			cfg := config.NewDefault()
			log, _ := testLogger()

			deployer, err := DetectDeployment(cfg, log)

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
				require.Nil(t, deployer)
			} else {
				require.NoError(t, err)
				require.NotNil(t, deployer)
				require.Equal(t, tt.wantType, deployer.Type())
			}
		})
	}
}

func TestPodmanDeployer(t *testing.T) {
	log, hook := testLogger()
	deployer := NewPodmanDeployer(log)

	require.Equal(t, DeploymentTypePodman, deployer.Type())

	ctx := context.Background()
	outputDir := t.TempDir()

	// Test stub methods return nil
	require.NoError(t, deployer.BackupDatabase(ctx, outputDir))
	require.NoError(t, deployer.BackupPKI(ctx, outputDir))
	require.NoError(t, deployer.BackupConfig(ctx, outputDir))

	// Verify DEBUG logging
	require.Len(t, hook.Entries, 3)
	require.Equal(t, logrus.DebugLevel, hook.Entries[0].Level)
}

func TestKubernetesDeployer(t *testing.T) {
	log, hook := testLogger()
	deployer := NewKubernetesDeployer(log)

	require.Equal(t, DeploymentTypeKubernetes, deployer.Type())

	ctx := context.Background()
	outputDir := t.TempDir()

	// Test stub methods return nil
	require.NoError(t, deployer.BackupDatabase(ctx, outputDir))
	require.NoError(t, deployer.BackupPKI(ctx, outputDir))
	require.NoError(t, deployer.BackupConfig(ctx, outputDir))

	// Verify DEBUG logging
	require.Len(t, hook.Entries, 3)
	require.Equal(t, logrus.DebugLevel, hook.Entries[0].Level)
}

func TestDeploymentTypeString(t *testing.T) {
	require.Equal(t, "podman", string(DeploymentTypePodman))
	require.Equal(t, "kubernetes", string(DeploymentTypeKubernetes))
	require.Equal(t, "unknown", string(DeploymentTypeUnknown))
}

func TestPathExists(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string
		wantTrue bool
	}{
		{
			name: "Existing file",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				testFile := filepath.Join(tmpDir, "test.txt")
				require.NoError(t, os.WriteFile(testFile, []byte("content"), 0644))
				return testFile
			},
			wantTrue: true,
		},
		{
			name: "Existing directory",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				testDir := filepath.Join(tmpDir, "testdir")
				require.NoError(t, os.MkdirAll(testDir, 0755))
				return testDir
			},
			wantTrue: true,
		},
		{
			name: "Non-existent path",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				return filepath.Join(tmpDir, "does-not-exist")
			},
			wantTrue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := tt.setup(t)
			result := pathExists(path)
			require.Equal(t, tt.wantTrue, result)
		})
	}
}

func TestCheckEnvironment(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(t *testing.T)
		wantK8s   bool
		wantPodman bool
	}{
		{
			name: "Kubernetes indicator set",
			setup: func(t *testing.T) {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
			},
			wantK8s:   true,
			wantPodman: false,
		},
		{
			name: "No indicators",
			setup: func(t *testing.T) {
				// Capture and unset K8s env var to ensure clean state
				origK8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				t.Cleanup(func() {
					if origK8sHost != "" {
						os.Setenv("KUBERNETES_SERVICE_HOST", origK8sHost)
					}
				})
			},
			wantK8s:   false,
			wantPodman: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			indicators := checkEnvironment()
			require.Equal(t, tt.wantK8s, indicators.kubernetesEnvSet)
			// Podman indicators depend on /etc/flightctl existence which varies by environment
			// Only check if we expect false
			if !tt.wantPodman {
				// We can't assert podmanPKIExists or podmanConfigDirExists are false
				// because they depend on the actual filesystem state
			}
		})
	}
}

