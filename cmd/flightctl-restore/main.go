package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/cache"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/sirupsen/logrus"
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
		Use:   "flightctl-restore [flags]",
		Short: "flightctl-restore prepares devices after database restoration.",
		Long: `flightctl-restore prepares devices after database restoration.

This command runs post-restoration device preparation tasks including:
- Initializing database and KV store connections
- Setting up organization resolvers and caches
- Preparing devices for normal operation after restore

The command should be run after restoring the database from a backup.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd.Context())
		},
		SilenceUsage: true,
	}

	// Add version command
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

func runRestore(ctx context.Context) error {
	// Bypass span check for restore operations
	ctx = store.WithBypassSpanCheck(ctx)

	log := log.InitLogs()
	log.Println("Starting Flight Control restore preparation")
	defer log.Println("Flight Control restore preparation completed")

	cfg, err := config.LoadOrGenerate(config.ConfigFile())
	if err != nil {
		log.Fatalf("reading configuration: %v", err)
	}
	log.Printf("Using config: %s", cfg)

	logLvl, err := logrus.ParseLevel(cfg.Service.LogLevel)
	if err != nil {
		logLvl = logrus.InfoLevel
	}
	log.SetLevel(logLvl)

	tracerShutdown := instrumentation.InitTracer(log, cfg, "flightctl-restore")
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

	log.Println("Creating store and service handler")
	storeInst := store.NewStore(db, log)

	orgCache := cache.NewOrganizationTTL(cache.DefaultTTL)
	go orgCache.Start()
	defer orgCache.Stop()

	serviceHandler := service.NewServiceHandler(storeInst, nil, kvStore, nil, log, "", "", []string{})

	log.Println("Running post-restoration device preparation")
	if err := serviceHandler.PrepareDevicesAfterRestore(ctx); err != nil {
		log.Fatalf("preparing devices after restore: %v", err)
	}

	log.Println("Post-restoration device preparation completed successfully")
	return nil
}
