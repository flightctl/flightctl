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
	var outputPath string
	var deploymentType string
	var namespace string
	var internalNamespace string
	var helmReleaseName string
	var dbContainerName string
	var dbName string
	var kvContainerName string

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
			return runBackup(cmd.Context(), outputPath, deploymentType, namespace, internalNamespace, helmReleaseName, dbContainerName, dbName, kvContainerName)
		},
		SilenceUsage: true,
	}

	cmd.Flags().StringVar(&outputPath, "output", ".",
		"Directory path where backup files will be written")
	cmd.Flags().StringVar(&deploymentType, "deployment-type", "",
		"Override deployment type detection (kubernetes or podman)")
	cmd.Flags().StringVar(&namespace, "namespace", "",
		"Kubernetes namespace for PKI secrets (default: flightctl)")
	cmd.Flags().StringVar(&internalNamespace, "internal-namespace", "",
		"Kubernetes namespace for DB pod (default: same as --namespace)")
	cmd.Flags().StringVar(&helmReleaseName, "helm-release-name", "",
		"Helm release name (default: flightctl)")
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
		Short: "Print flightctl-backup version information.",
		Run: func(cmd *cobra.Command, args []string) {
			clientVersion := version.Get()
			fmt.Fprintf(cmd.OutOrStdout(), "Flight Control Backup Version: %s\n", clientVersion.String())
		},
		SilenceUsage: true,
	}
	return cmd
}

func runBackup(ctx context.Context, outputPath, deploymentType, namespace, internalNamespace, helmReleaseName, dbContainerName, dbName, kvContainerName string) error {
	if deploymentType != "" {
		if err := backup.ValidateDeploymentType(deploymentType); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(outputPath, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	cfg := config.NewDefault()
	logger := log.InitLogs(cfg.Service.LogLevel)
	logger.Println("Starting Flight Control backup operation")
	defer logger.Println("Flight Control backup operation completed")
	logger.Printf("Output directory: %s", outputPath)

	tracerShutdown := tracing.InitTracer(logger, cfg, "flightctl-backup")
	defer func() {
		if err := tracerShutdown(ctx); err != nil {
			logger.Printf("Failed to shut down tracer: %v", err)
		}
	}()

	var err error
	var dt backup.DeploymentType
	if deploymentType != "" {
		dt = backup.DeploymentType(deploymentType)
	} else {
		dt, err = backup.DetectDeployment()
		if err != nil {
			return fmt.Errorf("detecting deployment type: %w", err)
		}
	}

	var deployer backup.Deployer
	switch dt {
	case backup.DeploymentTypeKubernetes:
		var k8sOpts []backup.KubernetesDeployerOption
		if namespace != "" {
			k8sOpts = append(k8sOpts, backup.WithNamespace(namespace))
		}
		if internalNamespace != "" {
			k8sOpts = append(k8sOpts, backup.WithInternalNamespace(internalNamespace))
		}
		if helmReleaseName != "" {
			k8sOpts = append(k8sOpts, backup.WithHelmReleaseName(helmReleaseName))
		}
		deployer = backup.NewKubernetesDeployer(logger, k8sOpts...)
	case backup.DeploymentTypePodman:
		var podmanOpts []backup.PodmanDeployerOption
		if dbContainerName != "" {
			podmanOpts = append(podmanOpts, backup.WithDBContainerName(dbContainerName))
		}
		if dbName != "" {
			podmanOpts = append(podmanOpts, backup.WithDBName(dbName))
		}
		if kvContainerName != "" {
			podmanOpts = append(podmanOpts, backup.WithKVContainerName(kvContainerName))
		}
		deployer = backup.NewPodmanDeployer(logger, podmanOpts...)
	default:
		return fmt.Errorf("unknown deployment type %q", dt)
	}
	logger.Printf("Deployment type: %s", deployer.Type())

	archivePath, err := backup.PerformBackup(ctx, deployer, outputPath, logger)
	if err != nil {
		return fmt.Errorf("backup failed: %w", err)
	}

	logger.Printf("Backup completed successfully: %s", archivePath)

	return nil
}
