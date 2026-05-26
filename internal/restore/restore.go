package restore

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/internal/backup"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/sirupsen/logrus"
)

// Restore executes the restore workflow:
//  1. Verifies SHA256 checksum of the archive (verifyChecksum)
//  2. Extracts the archive to a temporary directory (ExtractArchive)
//  3. Reads and validates metadata.json (ReadMetadata)
//  4. Validates the archive's deployment type against currentDeploymentType (ValidateDeploymentType)
//  5. Runs post-restoration device preparation (PrepareDevices)
//
// rs and kv must be pre-initialized by the caller. This allows integration
// tests to supply their own DB/KV connections without going through config.
// Deployment type detection is also the caller's responsibility.
//
// The temporary extraction directory is always cleaned up before Restore
// returns, whether on success or failure.
func Restore(
	ctx context.Context,
	archivePath string,
	currentDeploymentType backup.DeploymentType,
	rs *RestoreStore,
	kv kvstore.KVStore,
	log logrus.FieldLogger,
) error {
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
	if err := ValidateDeploymentType(metadata, currentDeploymentType); err != nil {
		return err
	}

	log.Info("Running post-restoration device preparation")
	if _, err := PrepareDevices(ctx, rs, kv, log); err != nil {
		return fmt.Errorf("post-restoration device preparation failed: %w", err)
	}

	return nil
}
