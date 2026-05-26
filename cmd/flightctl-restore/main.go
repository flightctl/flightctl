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

  1. Verifies the SHA256 checksum of the archive
  2. Extracts the archive to a temporary directory
  3. Reads and validates archive metadata (deployment type compatibility)
  4. Prepares devices for reconnection after restore

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

	log.Println("Detecting current deployment type")
	deployer, err := backup.DetectDeployment(cfg, log, "")
	if err != nil {
		return fmt.Errorf("failed to detect current deployment type: %w", err)
	}
	log.Printf("Current deployment type: %s", deployer.Type())

	tracerShutdown := tracing.InitTracer(log, cfg, "flightctl-restore")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			log.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	log.Println("Initializing database connection")
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

	log.Println("Initializing KV store connection")
	kvStore, err := kvstore.NewKVStore(ctx, log, cfg.KV.Hostname, cfg.KV.Port, cfg.KV.Password)
	if err != nil {
		log.Fatalf("initializing KV store: %v", err)
	}
	defer kvStore.Close()

	return restore.Restore(ctx, archivePath, deployer.Type(), restore.NewRestoreStore(db), kvStore, log)
}
