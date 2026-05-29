package main

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
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
	var deploymentType string
	var namespace string
	var internalNamespace string
	var keepOldDB bool
	var dbContainerName string
	var dbName string
	var kvContainerName string

	cmd := &cobra.Command{
		Use:   "flightctl-restore <archive-path> [flags]",
		Short: "flightctl-restore restores Flight Control state from a backup archive.",
		Long: `flightctl-restore restores Flight Control state from a backup archive.

This command restores a Flight Control server from a backup archive produced
by flightctl-backup. It performs the following steps:

  1. Verifies the SHA256 checksum of the archive
  2. Extracts the archive to a temporary directory
  3. Reads and validates archive metadata (deployment type compatibility)
  4. Stops FlightCtl services (systemd units or Kubernetes Deployments)
  5. Restores the database from the archive dump
  6. Starts FlightCtl services again (always, even on failure)
  7. Prepares devices for reconnection after restore

WARNING: FlightCtl services will be stopped for the duration of the restore.
Plan for a service outage before running this command in production.

The archive-path argument is required and must point to a .tar.gz archive
created by flightctl-backup.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestore(cmd.Context(), args[0], deploymentType, namespace, internalNamespace, keepOldDB, dbContainerName, dbName, kvContainerName)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&deploymentType, "deployment-type", "",
		"Override deployment type detection (kubernetes or podman)")
	cmd.Flags().StringVar(&namespace, "namespace", "",
		"Kubernetes external namespace for api/ui deployments (default: flightctl)")
	cmd.Flags().StringVar(&internalNamespace, "internal-namespace", "",
		"Kubernetes internal namespace for worker/periodic/db deployments (default: namespace)")
	cmd.Flags().BoolVar(&keepOldDB, "keep-old-db", false,
		"Keep the pre-restore database renamed as <dbname>_old_<timestamp> instead of dropping it")
	cmd.Flags().StringVar(&dbContainerName, "db-container-name", "",
		"Podman database container name (default: flightctl-db)")
	cmd.Flags().StringVar(&dbName, "db-name", "",
		"Podman database name override (default: from service config)")
	cmd.Flags().StringVar(&kvContainerName, "kv-container-name", "",
		"Podman KV container name (default: flightctl-kv)")

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

func runRestore(ctx context.Context, archivePath, deploymentType, namespace, internalNamespace string, keepOldDB bool, dbContainerName, dbName, kvContainerName string) error {
	if deploymentType != "" {
		if err := backup.ValidateDeploymentType(deploymentType); err != nil {
			return err
		}
	}

	ctx = store.WithBypassSpanCheck(ctx)

	cfg := config.NewDefault()
	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Println("Starting Flight Control restore")
	defer logger.Println("Flight Control restore completed")

	var err error
	var dt backup.DeploymentType
	if deploymentType != "" {
		dt = backup.DeploymentType(deploymentType)
	} else {
		logger.Println("Detecting current deployment type")
		dt, err = backup.DetectDeployment()
		if err != nil {
			return fmt.Errorf("failed to detect current deployment type: %w", err)
		}
	}
	logger.Printf("Deployment type: %s", dt)

	var restoreDeployer restore.Deployer
	switch dt {
	case backup.DeploymentTypeKubernetes:
		var k8sOpts []restore.KubernetesRestoreOption
		if namespace != "" {
			k8sOpts = append(k8sOpts, restore.WithRestoreNamespace(namespace))
		}
		if internalNamespace != "" {
			k8sOpts = append(k8sOpts, restore.WithRestoreInternalNamespace(internalNamespace))
		}
		k8sOpts = append(k8sOpts, restore.WithKeepOldDB(keepOldDB))
		restoreDeployer = restore.NewKubernetesRestoreDeployer(logger, k8sOpts...)
	default:
		var podmanOpts []restore.PodmanRestoreOption
		podmanOpts = append(podmanOpts, restore.WithPodmanKeepOldDB(keepOldDB))
		if dbContainerName != "" {
			podmanOpts = append(podmanOpts, restore.WithDBContainerName(dbContainerName))
		}
		if dbName != "" {
			podmanOpts = append(podmanOpts, restore.WithDBName(dbName))
		}
		if kvContainerName != "" {
			podmanOpts = append(podmanOpts, restore.WithKVContainerName(kvContainerName))
		}
		restoreDeployer = restore.NewPodmanRestoreDeployer(logger, podmanOpts...)
	}

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-restore")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			logger.Fatalf("failed to shut down tracer: %v", err)
		}
	}()

	return restore.Restore(ctx, archivePath, restoreDeployer, logger)
}
