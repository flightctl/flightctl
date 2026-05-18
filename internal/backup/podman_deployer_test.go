package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/internal/config"
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

	deployer := NewPodmanDeployer(cfg, log)
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
	deployer := NewPodmanDeployer(cfg, log)
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
			deployer := NewPodmanDeployer(cfg, log)
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
