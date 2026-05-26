package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKubernetesDeployer_BackupDatabase_ExternalDB(t *testing.T) {
	// Create config with external database
	cfg := config.NewDefault()
	cfg.Database.Hostname = "db.example.com"
	cfg.Database.Port = 5432
	cfg.Database.User = "testuser"
	cfg.Database.Name = "testdb"
	cfg.Database.Password = "testpass"

	log, _ := test.NewNullLogger()

	deployer := NewKubernetesDeployer(cfg, log, "", "", "", nil)
	ctx := context.Background()
	outputDir := t.TempDir()

	// Execute backup
	err := deployer.BackupDatabase(ctx, outputDir)

	// Should return ErrExternalDatabase
	require.ErrorIs(t, err, ErrExternalDatabase)

	// Should NOT create db directory or dump file
	dbDir := filepath.Join(outputDir, "db")
	_, err = os.Stat(dbDir)
	require.True(t, os.IsNotExist(err), "db directory should not be created for external DB")
}

func TestKubernetesDeployer_BackupDatabase_InternalDB_DirectoryCreation(t *testing.T) {
	// Create config with internal database
	cfg := config.NewDefault()
	cfg.Database.Hostname = "flightctl-db"
	cfg.Database.Port = 5432
	cfg.Database.User = "flightctl"
	cfg.Database.Name = "flightctl"
	cfg.Database.Password = "password"

	log, _ := test.NewNullLogger()
	deployer := NewKubernetesDeployer(cfg, log, "", "", "", nil)
	ctx := context.Background()
	outputDir := t.TempDir()

	// Mock kubectl by environment - test expects command to fail
	// This test verifies directory creation happens before command execution

	_ = deployer.BackupDatabase(ctx, outputDir)

	// Directory should be created even if kubectl commands fail
	dbDir := filepath.Join(outputDir, "db")
	stat, statErr := os.Stat(dbDir)
	require.NoError(t, statErr, "db directory should be created even if kubectl fails")
	require.True(t, stat.IsDir(), "db should be a directory")
}

func TestKubernetesDeployer_BackupDatabase_CommandConstruction(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		user     string
		dbname   string
	}{
		{
			name:     "flightctl-db with default settings",
			hostname: "flightctl-db",
			user:     "flightctl",
			dbname:   "flightctl",
		},
		{
			name:     "flightctl-db.flightctl-internal FQDN",
			hostname: "flightctl-db.flightctl-internal",
			user:     "postgres",
			dbname:   "flightctl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Database.Hostname = tt.hostname
			cfg.Database.User = tt.user
			cfg.Database.Name = tt.dbname
			cfg.Database.Password = "testpass"

			log, _ := test.NewNullLogger()
			deployer := NewKubernetesDeployer(cfg, log, "", "", "", nil)
			ctx := context.Background()
			outputDir := t.TempDir()

			// Execute - will fail since Kubernetes is not available in test environment
			err := deployer.BackupDatabase(ctx, outputDir)

			// Verify db directory was created (this happens before Kubernetes client operations)
			dbDir := filepath.Join(outputDir, "db")
			stat, statErr := os.Stat(dbDir)
			require.NoError(t, statErr, "db directory should be created")
			require.True(t, stat.IsDir())

			// Error expected since Kubernetes cluster is not available or pod doesn't exist
			// The actual command execution and success path will be tested in integration/e2e tests
			require.Error(t, err, "should fail when Kubernetes cluster is not available")
		})
	}
}

func TestKubernetesDeployer_BackupPKI_NoDirectoryOnValidationFailure(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Use fake clientset with explicit namespace (no Secrets in fake cluster)
	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(cfg, log, "nonexistent-namespace", "", "", fakeClient)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupPKI will fail during validation (Secret not found in fake cluster)
	err := deployer.BackupPKI(ctx, outputDir)

	// Verify error is returned
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to verify PKI secrets exist")

	// Verify PKI directory was NOT created (validation fails before directory creation)
	pkiDir := filepath.Join(outputDir, "pki")
	_, statErr := os.Stat(pkiDir)
	require.True(t, os.IsNotExist(statErr), "PKI directory should not be created when validation fails")
}

func TestKubernetesDeployer_BackupConfig_NoHelmSecrets(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Use fake clientset with no Helm Secrets
	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(cfg, log, "flightctl", "", "", fakeClient)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig should fail when no deployed Helm release Secret found
	err := deployer.BackupConfig(ctx, outputDir)

	// Verify error about missing deployed Helm Secret
	require.Error(t, err)
	require.Contains(t, err.Error(), "no deployed Helm release Secret found")
}

func TestKubernetesDeployer_BackupConfig_DirectoryPermissions(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Use fake clientset with no Secrets (will fail, but config dir should be created first)
	fakeClient := fake.NewSimpleClientset()
	deployer := NewKubernetesDeployer(cfg, log, "flightctl", "", "", fakeClient)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig creates config/ directory before checking for Helm Secrets
	_ = deployer.BackupConfig(ctx, outputDir)

	// Verify config directory is created with 0700 permissions
	configDir := filepath.Join(outputDir, "config")
	stat, err := os.Stat(configDir)
	require.NoError(t, err, "config directory should be created")
	require.True(t, stat.IsDir(), "config should be a directory")
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "config directory should have 0700 permissions")
}

func TestKubernetesDeployer_BackupConfig_Success(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Create mock Helm release Secrets (Helm 3 format)
	// Older revision
	helmSecret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "1",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("base64+gzip+json encoded release data"),
		},
	}

	// Newer revision (should be selected as deployed)
	helmSecret2 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v3",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "3",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("base64+gzip+json with chart+config+manifest"),
		},
	}

	// Use fake clientset with the Helm Secrets
	fakeClient := fake.NewSimpleClientset(helmSecret1, helmSecret2)
	deployer := NewKubernetesDeployer(cfg, log, "flightctl", "", "", fakeClient)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	outputDir := t.TempDir()

	// BackupConfig should succeed with Helm Secrets
	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	// Verify Helm Secret YAML file was created
	helmSecretPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, helmSecretPath)

	// Verify file permissions (0600 for sensitive files)
	stat, err := os.Stat(helmSecretPath)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), stat.Mode().Perm(), "Helm Secret YAML should have 0600 permissions")

	// Verify file contains Helm Secret data
	content, err := os.ReadFile(helmSecretPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "sh.helm.release.v1.flightctl.v3")
	require.Contains(t, string(content), "helm.sh/release.v1")
}

func TestKubernetesDeployer_BackupConfig_DeployedRevision(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Create multiple Helm release Secrets with different revisions
	// Only the one with status=deployed should be backed up
	helmSecret1 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "1",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("old revision"),
		},
	}

	helmSecret5 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v5",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "5",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("deployed revision"),
		},
	}

	helmSecret3 := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v3",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "3",
				"status":  "superseded",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("middle revision"),
		},
	}

	// Use fake clientset with Secrets in random order
	fakeClient := fake.NewSimpleClientset(helmSecret1, helmSecret5, helmSecret3)
	deployer := NewKubernetesDeployer(cfg, log, "flightctl", "", "", fakeClient)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig should select only the deployed revision
	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	// Verify the deployed release was backed up
	deployedPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, deployedPath)

	// Verify file contains the deployed Secret (with status=deployed label)
	content, err := os.ReadFile(deployedPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "sh.helm.release.v1.flightctl.v5")
	require.Contains(t, string(content), "status: deployed")
}

func TestKubernetesDeployer_BackupConfig_LabelFiltering(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Create FlightCtl Helm Secret with correct labels
	flightctlSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.flightctl.v2",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "flightctl",
				"version": "2",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("flightctl release data"),
		},
	}

	// Create Helm Secret for different release (should be ignored)
	otherSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sh.helm.release.v1.other-app.v1",
			Namespace: "flightctl",
			Labels: map[string]string{
				"owner":   "helm",
				"name":    "other-app",
				"version": "1",
				"status":  "deployed",
			},
		},
		Type: "helm.sh/release.v1",
		Data: map[string][]byte{
			"release": []byte("other app release data"),
		},
	}

	// Use fake clientset with both Secrets
	fakeClient := fake.NewSimpleClientset(flightctlSecret, otherSecret)
	deployer := NewKubernetesDeployer(cfg, log, "flightctl", "", "", fakeClient)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig should only backup the flightctl release
	err := deployer.BackupConfig(ctx, outputDir)
	require.NoError(t, err)

	// Verify only flightctl release was backed up
	flightctlPath := filepath.Join(outputDir, "config", "helm-release-flightctl.yaml")
	require.FileExists(t, flightctlPath)

	// Verify other release was NOT backed up
	otherPath := filepath.Join(outputDir, "config", "helm-release-other-app.yaml")
	require.NoFileExists(t, otherPath)
}
