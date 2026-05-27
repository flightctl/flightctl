package restore

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/sirupsen/logrus"
)

// startServicesTimeout is the deadline given to StartServices in the deferred
// restart so the caller always gets an error back within a predictable window.
const startServicesTimeout = 2 * time.Minute

// Restore executes the full restore workflow:
//  1. Verifies SHA256 checksum of the archive (verifyChecksum)
//  2. Extracts the archive to a temporary directory (ExtractArchive)
//  3. Reads and validates metadata.json (ReadMetadata)
//  4. Validates the archive's deployment type against deployer.Type() (ValidateDeploymentType)
//  5. Stops FlightCtl services (deployer.StopServices)
//  6. Imports the database (deployer.RestoreDatabase)
//  7. Restores PKI materials (deployer.RestorePKI)
//  8. Restores service configuration (deployer.RestoreConfig)
//  9. Retrieves service credentials via deployer.GetConfig
//  10. Exposes DB and KV via deployer.ExposeService (no-op for Podman, port-forward for Kubernetes)
//  11. Runs post-restoration device preparation (PrepareDevices)
//  12. Starts FlightCtl services (deployer.StartServices) — deferred, always runs even on failure
//
// The deployer encapsulates all deployment-specific operations and credential extraction.
// The temporary extraction directory is always cleaned up before Restore returns.
func Restore(
	ctx context.Context,
	archivePath string,
	deployer Deployer,
	log *logrus.Logger,
) (retErr error) {
	log.Infof("Verifying checksum of archive %q", archivePath)
	if err := verifyChecksum(archivePath); err != nil {
		return fmt.Errorf("archive integrity check failed: %w", err)
	}

	log.Infof("Extracting archive %q", archivePath)
	extractDir, err := ExtractArchive(ctx, archivePath)
	defer func() {
		if extractDir != "" {
			if removeErr := os.RemoveAll(extractDir); removeErr != nil {
				log.Warnf("Failed to clean up extraction directory %s: %v", extractDir, removeErr)
			}
		}
	}()
	if err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	log.Info("Reading archive metadata")
	metadata, err := ReadMetadata(extractDir)
	if err != nil {
		return fmt.Errorf("failed to read archive metadata: %w", err)
	}
	log.Infof("Archive metadata: version=%s, deploymentType=%s, databaseIncluded=%v, timestamp=%s",
		metadata.Version, metadata.DeploymentType, metadata.DatabaseIncluded,
		metadata.Timestamp.Format("2006-01-02T15:04:05Z"))

	log.Info("Validating deployment type compatibility")
	if err := ValidateDeploymentType(metadata, deployer.Type()); err != nil {
		return err
	}

	// Register StartServices before calling StopServices so that any deployments
	// already scaled down during a partial Kubernetes stop are always recovered,
	// even if StopServices returns an error mid-iteration.
	//
	// The named return retErr lets the deferred closure merge a restart failure
	// into the function's return value rather than only logging it.
	defer func() {
		log.Info("Starting FlightCtl services")
		startCtx, cancel := context.WithTimeout(context.Background(), startServicesTimeout)
		defer cancel()
		startErr := deployer.StartServices(startCtx)
		if startErr == nil {
			return
		}
		log.Warnf("Failed to start FlightCtl services (manual restart required): %v", startErr)
		if retErr == nil {
			retErr = fmt.Errorf("failed to start FlightCtl services: %w", startErr)
		} else {
			retErr = fmt.Errorf("%w; additionally failed to start FlightCtl services: %v", retErr, startErr)
		}
	}()

	log.Info("Stopping FlightCtl services")
	if err := deployer.StopServices(ctx); err != nil {
		return fmt.Errorf("failed to stop FlightCtl services: %w", err)
	}

	log.Info("Restoring database")
	if err := deployer.RestoreDatabase(ctx, extractDir); err != nil {
		return fmt.Errorf("database restore failed: %w", err)
	}
	log.Info("Database restore completed")

	log.Info("Restoring PKI materials")
	if err := deployer.RestorePKI(ctx, extractDir); err != nil {
		return fmt.Errorf("PKI restore failed: %w", err)
	}
	log.Info("PKI restore completed")

	log.Info("Restoring service configuration")
	if err := deployer.RestoreConfig(ctx, extractDir); err != nil {
		return fmt.Errorf("service configuration restore failed: %w", err)
	}
	log.Info("Service configuration restore completed")

	log.Info("Retrieving service credentials from infrastructure")
	cfg, err := deployer.GetConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to retrieve service credentials: %w", err)
	}

	log.Info("Exposing database for post-restoration device preparation")
	dbHost, dbPort, dbCleanup, err := deployer.ExposeService(ctx, "flightctl-db")
	if err != nil {
		return fmt.Errorf("failed to expose database service: %w", err)
	}
	defer dbCleanup()

	log.Info("Exposing KV store for post-restoration device preparation")
	kvHost, kvPort, kvCleanup, err := deployer.ExposeService(ctx, "flightctl-kv")
	if err != nil {
		return fmt.Errorf("failed to expose KV service: %w", err)
	}
	defer kvCleanup()

	exposedCfg := *cfg
	exposedCfg.Database.Hostname = dbHost
	exposedCfg.Database.Port = uint(dbPort)
	exposedCfg.KV.Hostname = kvHost
	exposedCfg.KV.Port = uint(kvPort)

	db, err := store.InitDB(&exposedCfg, log)
	if err != nil {
		return fmt.Errorf("failed to initialize database connection: %w", err)
	}
	defer func() {
		if sqlDB, closeErr := db.DB(); closeErr == nil {
			sqlDB.Close()
		}
	}()

	kv, err := kvstore.NewKVStore(ctx, log, kvHost, uint(kvPort), cfg.KV.Password)
	if err != nil {
		return fmt.Errorf("failed to initialize KV store connection: %w", err)
	}
	defer kv.Close()

	log.Info("Running post-restoration device preparation")
	if _, err := PrepareDevices(ctx, NewRestoreStore(db), kv, log); err != nil {
		return fmt.Errorf("post-restoration device preparation failed: %w", err)
	}

	return nil
}
