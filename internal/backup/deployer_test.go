package backup

import (
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
		setupEnv    func(t *testing.T) (basePath string, cleanup func())
		wantType    DeploymentType
		wantErr     bool
		errContains string
	}{
		{
			name: "Podman deployment - PKI exists",
			setupEnv: func(t *testing.T) (string, func()) {
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

				return testFlightctlDir, func() {
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
			setupEnv: func(t *testing.T) (string, func()) {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				// Return empty basePath to use default
				return "", func() {}
			},
			wantType: DeploymentTypeKubernetes,
			wantErr:  false,
		},
		{
			name: "No deployment detected",
			setupEnv: func(t *testing.T) (string, func()) {
				// Capture and unset K8s env var to ensure clean state
				origK8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_HOST")

				// Use temp directory without flightctl indicators
				// This prevents test failure if developer has /etc/flightctl on their system
				tmpDir := t.TempDir()
				testFlightctlDir := filepath.Join(tmpDir, "flightctl-nonexistent")

				return testFlightctlDir, func() {
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
			setupEnv: func(t *testing.T) (string, func()) {
				// Create test-local directory structure
				tmpDir := t.TempDir()
				testFlightctlDir := filepath.Join(tmpDir, "flightctl")
				pkiDir := filepath.Join(testFlightctlDir, "pki")
				require.NoError(t, os.MkdirAll(pkiDir, 0755))
				caCrt := filepath.Join(pkiDir, "ca.crt")
				require.NoError(t, os.WriteFile(caCrt, []byte("test"), 0644))

				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")

				return testFlightctlDir, func() {}
			},
			wantType:    DeploymentTypeUnknown,
			wantErr:     true,
			errContains: "conflicting deployment indicators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			basePath, cleanup := tt.setupEnv(t)
			defer cleanup()

			cfg := config.NewDefault()
			log, _ := testLogger()

			deployer, err := DetectDeployment(cfg, log, basePath)

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
	cfg := config.NewDefault()
	cfg.Database.Hostname = "localhost"
	log, _ := testLogger()
	deployer := NewPodmanDeployer(cfg, log)

	require.Equal(t, DeploymentTypePodman, deployer.Type())

	// Verify config is set
	require.NotNil(t, deployer.cfg)
	require.Equal(t, cfg, deployer.cfg)
}

func TestKubernetesDeployer(t *testing.T) {
	cfg := config.NewDefault()
	cfg.Database.Hostname = "flightctl-db"
	log, _ := testLogger()
	deployer := NewKubernetesDeployer(cfg, log, "")

	require.Equal(t, DeploymentTypeKubernetes, deployer.Type())

	// Verify config is set
	require.NotNil(t, deployer.cfg)
	require.Equal(t, cfg, deployer.cfg)
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

func TestIsInternalDB(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     bool
	}{
		{
			name:     "localhost is internal",
			hostname: "localhost",
			want:     true,
		},
		{
			name:     "127.0.0.1 is internal",
			hostname: "127.0.0.1",
			want:     true,
		},
		{
			name:     "flightctl-db is internal",
			hostname: "flightctl-db",
			want:     true,
		},
		{
			name:     "flightctl-db.flightctl-internal is internal",
			hostname: "flightctl-db.flightctl-internal",
			want:     true,
		},
		{
			name:     "flightctl-db with full DNS is internal",
			hostname: "flightctl-db.flightctl-internal.svc.cluster.local",
			want:     true,
		},
		{
			name:     "flightctl-db in custom namespace is internal",
			hostname: "flightctl-db.my-namespace",
			want:     true,
		},
		{
			name:     "external database hostname",
			hostname: "db.example.com",
			want:     false,
		},
		{
			name:     "external IP address",
			hostname: "192.168.1.10",
			want:     false,
		},
		{
			name:     "external cloud database",
			hostname: "mydb.rds.amazonaws.com",
			want:     false,
		},
		{
			name:     "empty hostname is external",
			hostname: "",
			want:     false,
		},
		{
			name:     "flightctl-db.evil.com should be rejected (security)",
			hostname: "flightctl-db.evil.com",
			want:     false,
		},
		{
			name:     "flightctl-db.namespace.evil.com should be rejected (security)",
			hostname: "flightctl-db.namespace.evil.com",
			want:     false,
		},
		{
			name:     "flightctl-db.namespace.svc.cluster.evil should be rejected (security)",
			hostname: "flightctl-db.namespace.svc.cluster.evil",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Database.Hostname = tt.hostname

			got := isInternalDB(cfg)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckEnvironment(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T) string
		wantK8s    bool
		wantPodman bool
	}{
		{
			name: "Kubernetes indicator set",
			setup: func(t *testing.T) string {
				t.Setenv("KUBERNETES_SERVICE_HOST", "kubernetes.default.svc")
				// Use non-existent path for clean test
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantK8s:    true,
			wantPodman: false,
		},
		{
			name: "No indicators",
			setup: func(t *testing.T) string {
				// Capture and unset K8s env var to ensure clean state
				origK8sHost := os.Getenv("KUBERNETES_SERVICE_HOST")
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
				t.Cleanup(func() {
					if origK8sHost != "" {
						os.Setenv("KUBERNETES_SERVICE_HOST", origK8sHost)
					}
				})
				// Use non-existent path for clean test
				return filepath.Join(t.TempDir(), "nonexistent")
			},
			wantK8s:    false,
			wantPodman: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			basePath := tt.setup(t)
			indicators := checkEnvironment(basePath)
			require.Equal(t, tt.wantK8s, indicators.kubernetesEnvSet)
			require.Equal(t, tt.wantPodman, indicators.podmanPKIExists)
			require.Equal(t, tt.wantPodman, indicators.podmanConfigDirExists)
		})
	}
}
