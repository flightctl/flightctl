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
		detector    *Detector
		wantType    DeploymentType
		wantErr     bool
		errContains string
	}{
		{
			name:     "Podman deployment - service active",
			detector: &Detector{PodmanChecker: func() bool { return true }, KubeconfigChecker: func() bool { return false }},
			wantType: DeploymentTypePodman,
		},
		{
			name:     "Kubernetes deployment - kubeconfig present",
			detector: &Detector{PodmanChecker: func() bool { return false }, KubeconfigChecker: func() bool { return true }},
			wantType: DeploymentTypeKubernetes,
		},
		{
			name:        "No deployment detected",
			detector:    &Detector{PodmanChecker: func() bool { return false }, KubeconfigChecker: func() bool { return false }},
			wantType:    DeploymentTypeUnknown,
			wantErr:     true,
			errContains: "unable to detect deployment type",
		},
		{
			name:        "Conflicting indicators",
			detector:    &Detector{PodmanChecker: func() bool { return true }, KubeconfigChecker: func() bool { return true }},
			wantType:    DeploymentTypeUnknown,
			wantErr:     true,
			errContains: "conflicting deployment indicators",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dt, err := tt.detector.Detect()

			if tt.wantErr {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.errContains)
				require.Equal(t, DeploymentTypeUnknown, dt)
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.wantType, dt)
			}
		})
	}
}

func TestPodmanDeployer(t *testing.T) {
	log, _ := testLogger()
	deployer := NewPodmanDeployer(log)

	require.Equal(t, DeploymentTypePodman, deployer.Type())
}

func TestKubernetesDeployer(t *testing.T) {
	log, _ := testLogger()
	deployer := NewKubernetesDeployer(log)

	require.Equal(t, DeploymentTypeKubernetes, deployer.Type())
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
