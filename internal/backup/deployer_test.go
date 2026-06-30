package backup

import (
	"os"
	"path/filepath"
	"testing"

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

func TestParseServiceConfigDB(t *testing.T) {
	tests := []struct {
		name     string
		yaml     string
		wantType string
		wantName string
	}{
		{
			name:     "external db type",
			yaml:     "db:\n  type: external\n  name: flightctl\n",
			wantType: "external",
			wantName: "flightctl",
		},
		{
			name:     "builtin db type",
			yaml:     "db:\n  type: builtin\n  name: mydb\n",
			wantType: "builtin",
			wantName: "mydb",
		},
		{
			name:     "missing db section",
			yaml:     "service:\n  address: :3443\n",
			wantType: "",
			wantName: "",
		},
		{
			name:     "missing type field",
			yaml:     "db:\n  name: flightctl\n",
			wantType: "",
			wantName: "flightctl",
		},
		{
			name:     "empty yaml",
			yaml:     "",
			wantType: "",
			wantName: "",
		},
		{
			name:     "invalid yaml",
			yaml:     ": invalid: yaml: [",
			wantType: "",
			wantName: "",
		},
		{
			name:     "type with whitespace",
			yaml:     "db:\n  type: \"  External  \"\n",
			wantType: "external",
			wantName: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log, _ := test.NewNullLogger()
			got := parseServiceConfigDB([]byte(tt.yaml), log)
			require.Equal(t, tt.wantType, got.Type)
			require.Equal(t, tt.wantName, got.Name)
		})
	}
}

