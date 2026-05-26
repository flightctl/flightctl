package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/spf13/cobra"
)

func main() {
	command := NewFlightCtlBackupCommand()
	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}

func NewFlightCtlBackupCommand() *cobra.Command {
	// Local variables to capture flag values
	var outputPath string
	var configPath string

	cmd := &cobra.Command{
		Use:   "flightctl-backup [flags]",
		Short: "flightctl-backup creates a backup of the Flight Control database.",
		Long: `flightctl-backup creates a backup of the Flight Control database.

This command performs backup operations including:
- Database backup to the specified output directory
- Configuration validation
- Backup metadata generation

The command should be run with appropriate database permissions.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Use flag values directly in closure
			return runBackup(cmd.Context(), outputPath, configPath)
		},
		SilenceUsage: true,
	}

	// Define flags with defaults
	cmd.Flags().StringVar(&outputPath, "output", ".",
		"Directory path where backup files will be written")
	cmd.Flags().StringVar(&configPath, "config", config.ConfigFile(),
		"Path to the service configuration file")

	// Add version command
	cmd.AddCommand(NewCmdVersion())

	return cmd
}

func NewCmdVersion() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print flightctl-backup version information.",
		Run: func(cmd *cobra.Command, args []string) {
			clientVersion := version.Get()
			fmt.Fprintf(cmd.OutOrStdout(), "Flight Control Backup Version: %s\n", clientVersion.String())
		},
		SilenceUsage: true,
	}
	return cmd
}

func runBackup(ctx context.Context, outputPath, configPath string) error {
	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Load configuration
	cfg, err := config.LoadOrGenerate(configPath)
	if err != nil {
		return fmt.Errorf("reading configuration: %w", err)
	}

	// Initialize logging
	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Println("Starting Flight Control backup operation")
	defer logger.Println("Flight Control backup operation completed")
	logger.Printf("Using config: %s", cfg)
	logger.Printf("Output directory: %s", outputPath)

	// Initialize tracing (matching restore pattern)
	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-backup")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			logger.Printf("Failed to shut down tracer: %v", err)
		}
	}()

	// Detect deployment type
	deployer, err := backup.DetectDeployment(cfg, logger, "")
	if err != nil {
		return fmt.Errorf("detecting deployment type: %w", err)
	}

	// Log detected deployment type at INFO level
	logger.Printf("Detected deployment type: %s", deployer.Type())

	// Perform backup
	archivePath, err := backup.PerformBackup(ctx, deployer, outputPath, logger)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	logger.Printf("Backup completed successfully: %s", archivePath)

	return nil
}
