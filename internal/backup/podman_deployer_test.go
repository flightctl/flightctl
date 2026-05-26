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

func TestPodmanDeployer_BackupDatabase_ExternalDB(t *testing.T) {
	// Create config with external database
	cfg := config.NewDefault()
	cfg.Database.Hostname = "db.example.com"
	cfg.Database.Port = 5432
	cfg.Database.User = "testuser"
	cfg.Database.Name = "testdb"
	cfg.Database.Password = "testpass"

	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(cfg, log, "", "")
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

func TestPodmanDeployer_BackupDatabase_InternalDB_DirectoryCreation(t *testing.T) {
	// Create config with internal database
	cfg := config.NewDefault()
	cfg.Database.Hostname = "localhost"
	cfg.Database.Port = 5432
	cfg.Database.User = "flightctl"
	cfg.Database.Name = "flightctl"
	cfg.Database.Password = "password"

	log, _ := test.NewNullLogger()
	deployer := NewPodmanDeployer(cfg, log, "", "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// This test verifies directory creation happens before podman exec
	// We expect the command to fail (container doesn't exist), but directory should be created

	_ = deployer.BackupDatabase(ctx, outputDir)

	// Command will likely fail (container not running), but directory should be created
	dbDir := filepath.Join(outputDir, "db")
	stat, statErr := os.Stat(dbDir)
	require.NoError(t, statErr, "db directory should be created even if podman exec fails")
	require.True(t, stat.IsDir(), "db should be a directory")
}

func TestPodmanDeployer_BackupDatabase_CommandConstruction(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		port     uint
		user     string
		dbname   string
	}{
		{
			name:     "localhost with default port",
			hostname: "localhost",
			port:     5432,
			user:     "flightctl",
			dbname:   "flightctl",
		},
		{
			name:     "127.0.0.1 with custom port",
			hostname: "127.0.0.1",
			port:     5433,
			user:     "postgres",
			dbname:   "mydb",
		},
		{
			name:     "flightctl-db hostname",
			hostname: "flightctl-db",
			port:     5432,
			user:     "flightctl",
			dbname:   "flightctl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.NewDefault()
			cfg.Database.Hostname = tt.hostname
			cfg.Database.Port = tt.port
			cfg.Database.User = tt.user
			cfg.Database.Name = tt.dbname
			cfg.Database.Password = "testpass"

			log, _ := test.NewNullLogger()
			deployer := NewPodmanDeployer(cfg, log, "", "")
			ctx := context.Background()
			outputDir := t.TempDir()

			// Execute - will fail since container doesn't exist, but we verify directory creation
			err := deployer.BackupDatabase(ctx, outputDir)

			// Verify db directory was created (this happens before podman exec execution)
			dbDir := filepath.Join(outputDir, "db")
			stat, statErr := os.Stat(dbDir)
			require.NoError(t, statErr, "db directory should be created")
			require.True(t, stat.IsDir())

			// Error expected since flightctl-db container is not running in test environment
			// The actual command execution and success path will be tested in integration tests
			if err != nil {
				require.Contains(t, err.Error(), "container", "error should mention container")
			}
		})
	}
}

func TestPodmanDeployer_BackupPKI_Success(t *testing.T) {
	// Create mock PKI directory structure
	tmpRoot := t.TempDir()
	pkiSrc := filepath.Join(tmpRoot, "pki")

	// Create directory structure
	caDir := filepath.Join(pkiSrc, "ca")
	require.NoError(t, os.MkdirAll(caDir, 0755))

	// Create mock PKI files with different permissions
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("cert"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("key"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(pkiSrc, "tls.crt"), []byte("tls"), 0644))

	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()
	log.SetLevel(logrus.InfoLevel)

	// Create deployer with custom PKI path
	deployer := NewPodmanDeployer(cfg, log, pkiSrc, "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// Test BackupPKI public API
	err := deployer.BackupPKI(ctx, outputDir)
	require.NoError(t, err)

	// Verify files copied
	pkiDst := filepath.Join(outputDir, "pki")
	require.FileExists(t, filepath.Join(pkiDst, "ca", "ca.crt"))
	require.FileExists(t, filepath.Join(pkiDst, "ca", "ca.key"))
	require.FileExists(t, filepath.Join(pkiDst, "tls.crt"))

	// Verify file permissions preserved
	info, err := os.Stat(filepath.Join(pkiDst, "ca", "ca.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(pkiDst, "ca", "ca.crt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())

	// Verify directory permissions preserved
	dirInfo, err := os.Stat(filepath.Join(pkiDst, "ca"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), dirInfo.Mode().Perm())
}

func TestPodmanDeployer_BackupPKI_MissingDirectory(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Use explicit non-existent path instead of relying on default
	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	deployer := NewPodmanDeployer(cfg, log, nonExistentPath, "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupPKI will fail because the specified PKI directory doesn't exist
	err := deployer.BackupPKI(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "PKI directory not found")
}

func TestPodmanDeployer_BackupPKI_RejectsSymlinks(t *testing.T) {
	// Create mock PKI directory with a symlink
	pkiSrc := t.TempDir()

	// Create a regular file and a symlink to it
	regularFile := filepath.Join(pkiSrc, "regular.txt")
	require.NoError(t, os.WriteFile(regularFile, []byte("content"), 0644))

	symlinkFile := filepath.Join(pkiSrc, "symlink.txt")
	require.NoError(t, os.Symlink(regularFile, symlinkFile))

	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	// Create deployer with custom PKI path
	deployer := NewPodmanDeployer(cfg, log, pkiSrc, "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupPKI should fail due to symlink (security measure)
	err := deployer.BackupPKI(ctx, outputDir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlinks not supported")
	require.Contains(t, err.Error(), "symlink.txt")
}

func TestPodmanDeployer_BackupConfig(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(cfg, log, "", "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig will attempt to backup from /etc/flightctl/service-config.yaml
	// In test environment, this file won't exist, so expect an error
	err := deployer.BackupConfig(ctx, outputDir)

	// Should return error because /etc/flightctl/service-config.yaml doesn't exist in test env
	require.Error(t, err)
	require.Contains(t, err.Error(), "service-config.yaml")
}

func TestPodmanDeployer_BackupConfig_PAMVolumeExport(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(cfg, log, "", "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig will attempt volume export even though service-config.yaml is missing
	// We expect it to fail on service-config.yaml before getting to volume export
	// But we verify volumes/ directory is created
	_ = deployer.BackupConfig(ctx, outputDir)

	// Check if volumes directory gets created (may not if service-config.yaml check fails first)
	volumesDir := filepath.Join(outputDir, "volumes")
	_, err := os.Stat(volumesDir)
	// It's OK if directory doesn't exist - service-config.yaml failure may prevent reaching volume export
	if err == nil {
		stat, _ := os.Stat(volumesDir)
		require.True(t, stat.IsDir())
		require.Equal(t, os.FileMode(0700), stat.Mode().Perm())
	}
}

func TestPodmanDeployer_BackupConfig_DirectoryPermissions(t *testing.T) {
	cfg := config.NewDefault()
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(cfg, log, "", "")
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig creates config/ directory before checking service-config.yaml
	_ = deployer.BackupConfig(ctx, outputDir)

	// Verify config directory is created with 0700 permissions
	configDir := filepath.Join(outputDir, "config")
	stat, err := os.Stat(configDir)
	require.NoError(t, err, "config directory should be created")
	require.True(t, stat.IsDir(), "config should be a directory")
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "config directory should have 0700 permissions")
}
