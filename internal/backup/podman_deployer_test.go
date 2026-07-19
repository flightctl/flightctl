package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
)

// writeServiceConfig saves cfg to a temporary service-config.yaml and returns its path.
func writeServiceConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-config.yaml")
	require.NoError(t, config.Save(cfg, path))
	return path
}

const testDBPassword api.SecureString = "x-not-a-real-credential" //nolint:gosec // G101: throwaway test value

// internalDBServiceConfig returns a config with an internal (localhost) database hostname.
func internalDBServiceConfig() *config.Config {
	cfg := config.NewDefault()
	cfg.Database.Hostname = "localhost"
	cfg.Database.Port = 5432
	cfg.Database.User = "flightctl"
	cfg.Database.Name = "flightctl"
	cfg.Database.Password = testDBPassword
	return cfg
}

func TestPodmanDeployer_BackupDatabase_ExternalDB(t *testing.T) {
	log, _ := test.NewNullLogger()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "service-config.yaml")
	rawYAML := `db:
  type: "external"
  name: flightctl
`
	require.NoError(t, os.WriteFile(cfgPath, []byte(rawYAML), 0600))

	deployer := NewPodmanDeployer(log, WithServiceConfigPath(cfgPath))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)

	require.ErrorIs(t, err, ErrExternalDatabase)

	dbDir := filepath.Join(outputDir, "db")
	_, err = os.Stat(dbDir)
	require.True(t, os.IsNotExist(err), "db directory should not be created for external DB")
}

func TestPodmanDeployer_BackupDatabase_InternalDB_DirectoryCreation(t *testing.T) {
	log, _ := test.NewNullLogger()
	cfgPath := writeServiceConfig(t, internalDBServiceConfig())

	deployer := NewPodmanDeployer(log, WithServiceConfigPath(cfgPath))
	ctx := context.Background()
	outputDir := t.TempDir()

	_ = deployer.BackupDatabase(ctx, outputDir)

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
			cfg.Database.Password = testDBPassword
			cfgPath := writeServiceConfig(t, cfg)

			log, _ := test.NewNullLogger()
			deployer := NewPodmanDeployer(log, WithServiceConfigPath(cfgPath))
			ctx := context.Background()
			outputDir := t.TempDir()

			err := deployer.BackupDatabase(ctx, outputDir)

			dbDir := filepath.Join(outputDir, "db")
			stat, statErr := os.Stat(dbDir)
			require.NoError(t, statErr, "db directory should be created")
			require.True(t, stat.IsDir())

			if err != nil {
				require.Contains(t, err.Error(), "container", "error should mention container")
			}
		})
	}
}

func TestPodmanDeployer_OptionDefaults(t *testing.T) {
	log, _ := test.NewNullLogger()
	d := NewPodmanDeployer(log)
	require.Equal(t, "flightctl-db", d.dbContainerName)
	require.Equal(t, "podman", d.containerCLI)
	require.Equal(t, "flightctl-kv", d.kvContainerName)
	require.Empty(t, d.dbName)
}

func TestPodmanDeployer_BackupDatabase_DBNameOverride(t *testing.T) {
	log, _ := test.NewNullLogger()
	cfg := internalDBServiceConfig()
	cfg.Database.Name = "from-config"
	cfgPath := writeServiceConfig(t, cfg)

	deployer := NewPodmanDeployer(log,
		WithServiceConfigPath(cfgPath),
		WithDBName("override-db"),
		WithDBContainerName("custom-db-container"),
	)
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)
	// podman/docker exec will fail in unit test; verify directory was created
	dbDir := filepath.Join(outputDir, "db")
	stat, statErr := os.Stat(dbDir)
	require.NoError(t, statErr)
	require.True(t, stat.IsDir())
	if err != nil {
		require.Contains(t, err.Error(), "custom-db-container")
	}
}

func TestPodmanDeployer_BackupDatabase_MissingServiceConfig(t *testing.T) {
	log, _ := test.NewNullLogger()
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	deployer := NewPodmanDeployer(log, WithServiceConfigPath(nonExistent))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupDatabase(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to read service configuration")
}

func TestPodmanDeployer_BackupPKI_Success(t *testing.T) {
	tmpRoot := t.TempDir()
	pkiSrc := filepath.Join(tmpRoot, "pki")

	caDir := filepath.Join(pkiSrc, "ca")
	require.NoError(t, os.MkdirAll(caDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(caDir, "ca.crt"), []byte("cert"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(caDir, "ca.key"), []byte("key"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(pkiSrc, "tls.crt"), []byte("tls"), 0644))

	log, _ := test.NewNullLogger()
	log.SetLevel(logrus.InfoLevel)

	deployer := NewPodmanDeployer(log, WithPKIPath(pkiSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupPKI(ctx, outputDir)
	require.NoError(t, err)

	pkiDst := filepath.Join(outputDir, "pki")
	require.FileExists(t, filepath.Join(pkiDst, "ca", "ca.crt"))
	require.FileExists(t, filepath.Join(pkiDst, "ca", "ca.key"))
	require.FileExists(t, filepath.Join(pkiDst, "tls.crt"))

	info, err := os.Stat(filepath.Join(pkiDst, "ca", "ca.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())

	info, err = os.Stat(filepath.Join(pkiDst, "ca", "ca.crt"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0644), info.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Join(pkiDst, "ca"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0755), dirInfo.Mode().Perm())
}

func TestPodmanDeployer_BackupPKI_MissingDirectory(t *testing.T) {
	log, _ := test.NewNullLogger()

	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	deployer := NewPodmanDeployer(log, WithPKIPath(nonExistentPath))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupPKI(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "PKI directory not found")
}

func TestPodmanDeployer_BackupPKI_RejectsSymlinks(t *testing.T) {
	pkiSrc := t.TempDir()

	regularFile := filepath.Join(pkiSrc, "regular.txt")
	require.NoError(t, os.WriteFile(regularFile, []byte("content"), 0644))

	symlinkFile := filepath.Join(pkiSrc, "symlink.txt")
	require.NoError(t, os.Symlink(regularFile, symlinkFile))

	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(log, WithPKIPath(pkiSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupPKI(ctx, outputDir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "symlinks not supported")
	require.Contains(t, err.Error(), "symlink.txt")
}

func TestPodmanDeployer_BackupConfig(t *testing.T) {
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(log)
	ctx := context.Background()
	outputDir := t.TempDir()

	// BackupConfig will attempt to backup from /etc/flightctl/service-config.yaml
	// In test environment, this file won't exist, so expect an error
	err := deployer.BackupConfig(ctx, outputDir)

	require.Error(t, err)
	require.Contains(t, err.Error(), "service-config.yaml")
}

func TestPodmanDeployer_BackupConfig_PAMVolumeExport(t *testing.T) {
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(log)
	ctx := context.Background()
	outputDir := t.TempDir()

	_ = deployer.BackupConfig(ctx, outputDir)

	volumesDir := filepath.Join(outputDir, "volumes")
	_, err := os.Stat(volumesDir)
	if err == nil {
		stat, _ := os.Stat(volumesDir)
		require.True(t, stat.IsDir())
		require.Equal(t, os.FileMode(0700), stat.Mode().Perm())
	}
}

func TestPodmanDeployer_BackupConfig_DirectoryPermissions(t *testing.T) {
	log, _ := test.NewNullLogger()

	deployer := NewPodmanDeployer(log)
	ctx := context.Background()
	outputDir := t.TempDir()

	_ = deployer.BackupConfig(ctx, outputDir)

	configDir := filepath.Join(outputDir, "config")
	stat, err := os.Stat(configDir)
	require.NoError(t, err, "config directory should be created")
	require.True(t, stat.IsDir(), "config should be a directory")
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "config directory should have 0700 permissions")
}

func TestPodmanDeployer_BackupEncryptionKeys_MissingDirectory(t *testing.T) {
	log, _ := test.NewNullLogger()

	nonExistentPath := filepath.Join(t.TempDir(), "does-not-exist")
	deployer := NewPodmanDeployer(log, WithEncryptionPath(nonExistentPath))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(ctx, outputDir)

	require.NoError(t, err, "missing encryption directory should not be an error")

	encDir := filepath.Join(outputDir, "encryption")
	_, statErr := os.Stat(encDir)
	require.True(t, os.IsNotExist(statErr), "encryption output directory should not be created when source is missing")
}

func TestPodmanDeployer_BackupEncryptionKeys_Success(t *testing.T) {
	tmpRoot := t.TempDir()
	encSrc := filepath.Join(tmpRoot, "encryption")
	require.NoError(t, os.MkdirAll(encSrc, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(encSrc, "encryption.key"), []byte("secret-key"), 0600))

	log, _ := test.NewNullLogger()
	log.SetLevel(logrus.InfoLevel)

	deployer := NewPodmanDeployer(log, WithEncryptionPath(encSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(ctx, outputDir)
	require.NoError(t, err)

	encDst := filepath.Join(outputDir, "encryption")
	require.FileExists(t, filepath.Join(encDst, "encryption.key"))

	data, err := os.ReadFile(filepath.Join(encDst, "encryption.key"))
	require.NoError(t, err)
	require.Equal(t, "secret-key", string(data))

	info, err := os.Stat(filepath.Join(encDst, "encryption.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestPodmanDeployer_BackupEncryptionKeys_DirectoryPermissions(t *testing.T) {
	tmpRoot := t.TempDir()
	encSrc := filepath.Join(tmpRoot, "encryption")
	require.NoError(t, os.MkdirAll(encSrc, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(encSrc, "key.dat"), []byte("k"), 0600))

	log, _ := test.NewNullLogger()
	deployer := NewPodmanDeployer(log, WithEncryptionPath(encSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	require.NoError(t, deployer.BackupEncryptionKeys(ctx, outputDir))

	encDst := filepath.Join(outputDir, "encryption")
	stat, err := os.Stat(encDst)
	require.NoError(t, err)
	require.True(t, stat.IsDir())
	require.Equal(t, os.FileMode(0700), stat.Mode().Perm(), "encryption directory should have 0700 permissions")
}

func TestPodmanDeployer_BackupEncryptionKeys_CleanupOnError(t *testing.T) {
	tmpRoot := t.TempDir()
	encSrc := filepath.Join(tmpRoot, "encryption")
	require.NoError(t, os.MkdirAll(encSrc, 0755))
	require.NoError(t, os.Symlink("/dev/null", filepath.Join(encSrc, "badlink")))

	log, _ := test.NewNullLogger()
	deployer := NewPodmanDeployer(log, WithEncryptionPath(encSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	err := deployer.BackupEncryptionKeys(ctx, outputDir)
	require.Error(t, err, "should fail when encountering a symlink")

	encDst := filepath.Join(outputDir, "encryption")
	_, statErr := os.Stat(encDst)
	require.True(t, os.IsNotExist(statErr), "encryption directory should be cleaned up on error")
}

func TestPodmanDeployer_BackupEncryptionKeys_PreservesPermissions(t *testing.T) {
	tmpRoot := t.TempDir()
	encSrc := filepath.Join(tmpRoot, "encryption")
	subDir := filepath.Join(encSrc, "subdir")
	require.NoError(t, os.MkdirAll(subDir, 0750))
	require.NoError(t, os.WriteFile(filepath.Join(subDir, "nested.key"), []byte("nested"), 0640))

	log, _ := test.NewNullLogger()
	deployer := NewPodmanDeployer(log, WithEncryptionPath(encSrc))
	ctx := context.Background()
	outputDir := t.TempDir()

	require.NoError(t, deployer.BackupEncryptionKeys(ctx, outputDir))

	encDst := filepath.Join(outputDir, "encryption")
	dirInfo, err := os.Stat(filepath.Join(encDst, "subdir"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0750), dirInfo.Mode().Perm())

	fileInfo, err := os.Stat(filepath.Join(encDst, "subdir", "nested.key"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0640), fileInfo.Mode().Perm())
}
