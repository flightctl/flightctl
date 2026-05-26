package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/restore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/spf13/cobra"
)

func main() {
	command := NewFlightCtlRestoreCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func NewFlightCtlRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "flightctl-restore <archive-path> [flags]",
		Short: "flightctl-restore restores Flight Control state from a backup archive.",
		Long: `flightctl-restore restores Flight Control state from a backup archive.

This command restores a Flight Control server from a backup archive produced
by flightctl-backup. It performs the following steps:

  1. Validates the archive path and extracts it to a temporary directory
  2. Reads and validates archive metadata (deployment type compatibility)
  3. Prepares devices for reconnection after restore

The archive-path argument is required and must point to a .tar.gz archive
created by flightctl-backup.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd.Context(), args[0])
		},
		SilenceUsage: true,
	}

	cmd.AddCommand(NewCmdVersion())

	return cmd
}

func NewCmdVersion() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print flightctl-restore version information.",
		Run: func(cmd *cobra.Command, args []string) {
			clientVersion := version.Get()
			fmt.Printf("Flight Control Restore Version: %s\n", clientVersion.String())
		},
		SilenceUsage: true,
	}
	return cmd
}

func runRestore(ctx context.Context, archivePath string) error {
	ctx = store.WithBypassSpanCheck(ctx)

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.InitLogs().Fatalf("reading configuration: %v", err)
	}

	log := log.InitLogs(cfg.Service.LogLevel)
	log.Println("Starting Flight Control restore")
	defer log.Println("Flight Control restore completed")
	log.Printf("Using config: %s", cfg)
	log.Printf("Restoring from archive: %s", archivePath)

	log.Println("Extracting backup archive")
	extractDir, err := restore.ExtractArchive(ctx, archivePath)
	defer func() {
		if extractDir != "" {
			if removeErr := os.RemoveAll(extractDir); removeErr != nil {
				log.Warnf("Failed to clean up temporary extraction directory %s: %v", extractDir, removeErr)
			}
		}
	}()
	if err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}
	log.Printf("Archive extracted to temporary directory: %s", extractDir)

	log.Println("Reading archive metadata")
	metadata, err := restore.ReadMetadata(extractDir)
	if err != nil {
		return fmt.Errorf("failed to read archive metadata: %w", err)
	}
	log.Printf("Archive metadata: version=%s, deploymentType=%s, databaseIncluded=%v, timestamp=%s",
		metadata.Version, metadata.DeploymentType, metadata.DatabaseIncluded, metadata.Timestamp.Format("2006-01-02T15:04:05Z"))

	log.Println("Detecting current deployment type")
	deployer, err := backup.DetectDeployment(cfg, log, "")
	if err != nil {
		return fmt.Errorf("failed to detect current deployment type: %w", err)
	}
	log.Printf("Current deployment type: %s", deployer.Type())

	if err := restore.ValidateDeploymentType(metadata, deployer.Type()); err != nil {
		return err
	}
	log.Println("Deployment type validation passed")

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-restore")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing database connection for restore operations")
	db, err := store.InitDB(cfg, log)
	if err != nil {
		log.Fatalf("initializing database: %v", err)
	}
	defer func() {
		if sqlDB, err := db.DB(); err != nil {
			log.Printf("Failed to get database connection for cleanup: %v", err)
		} else {
			sqlDB.Close()
		}
	}()

	log.Println("Initializing KV store connection for restore operations")
	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing KV store: %v", err)
	}
	defer kvStore.Close()

	log.Println("Running post-restoration device preparation")
	restoreStore := restore.NewRestoreStore(db)
	if _, err := restore.PrepareDevices(ctx, restoreStore, kvStore, log); err != nil {
		log.Fatalf("preparing devices after restore: %v", err)
	}

	log.Println("Post-restoration device preparation completed successfully")
	return nil
}
