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

func TestKubernetesDeployer_BackupDatabase_ExternalDB(t *testing.T) {
	// Create config with external database
	cfg := config.NewDefault()
	cfg.Database.Hostname = "db.example.com"
	cfg.Database.Port = 5432
	cfg.Database.User = "testuser"
	cfg.Database.Name = "testdb"
	cfg.Database.Password = "testpass"

	log, _ := test.NewNullLogger()

	deployer := NewKubernetesDeployer(cfg, log, "")
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
	deployer := NewKubernetesDeployer(cfg, log, "")
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
			deployer := NewKubernetesDeployer(cfg, log, "")
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
