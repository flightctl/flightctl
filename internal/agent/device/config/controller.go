package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	ignv3types "github.com/coreos/ignition/v2/config/v3_4/types"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	hookManager  hook.Manager
	deviceWriter fileio.Writer
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	hookManager hook.Manager,
	deviceWriter fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		hookManager:  hookManager,
		deviceWriter: deviceWriter,
		log:          log,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	// config
	if desired.Config != nil {
		c.log.Debug("syncing config data")
		return c.ensureConfigData(ctx, util.FromPtr(current.Config), util.FromPtr(desired.Config))
	} else {
		// garbage collect all ignition files defined in the current spec
		ignitionConfig, err := ParseAndConvertConfigFromStr(util.FromPtr(current.Config))
		if err != nil {
			return err
		}
		if err := c.removeObsoleteFiles(ctx, ignitionConfig.Storage.Files, []ignv3types.File{}); err != nil {
			return fmt.Errorf("failed to garbage collect stale files: %w", err)
		}
	}

	return nil
}

func computeRemoval(currentFileList, desiredFileList []ignv3types.File) []string {
	desiredFiles := getFilePaths(desiredFileList)
	result := []string{}
	desiredMap := make(map[string]bool)

	for _, file := range desiredFiles {
		desiredMap[file] = true
	}

	currentFiles := getFilePaths(currentFileList)
	for _, file := range currentFiles {
		if !desiredMap[file] {
			result = append(result, file)
		}
	}

	return result
}

func (c *Controller) ensureConfigData(ctx context.Context, currentData, desiredData string) error {
	currentIgnition, err := ParseAndConvertConfigFromStr(currentData)
	if err != nil {
		c.log.Warnf("failed to parse current ignition: %+v", err)
	}
	desiredIgnition, err := ParseAndConvertConfigFromStr(desiredData)
	if err != nil {
		c.log.Warnf("failed to parse desired config: %+v", err)
		return err
	}

	if err := c.removeObsoleteFiles(ctx, currentIgnition.Storage.Files, desiredIgnition.Storage.Files); err != nil {
		return fmt.Errorf("failed to remove obsolete files: %w", err)
	}

	if len(desiredIgnition.Storage.Files) == 0 {
		// no files to write
		return nil
	}

	// write ignition files to disk and trigger pre hooks
	c.log.Debug("Writing ignition files")
	err = c.WriteIgnitionFiles(ctx, desiredIgnition.Storage.Files)
	if err != nil {
		c.log.Warnf("Writing ignition files failed: %+v", err)
		return fmt.Errorf("failed to apply configuration: %w", err)
	}
	return nil
}

// removeObsoleteFiles removes files that are present in the currentFiles but not in the desiredFiles.
func (c *Controller) removeObsoleteFiles(ctx context.Context, currentFiles, desiredFiles []ignv3types.File) error {
	removeFiles := computeRemoval(currentFiles, desiredFiles)
	for _, file := range removeFiles {
		c.log.Infof("Deleting file: %s", file)
		// trigger delete pre hook and wait for it to complete
		c.hookManager.OnBeforeRemove(ctx, file)
		if err := c.deviceWriter.RemoveFile(file); err != nil {
			return fmt.Errorf("deleting files failed: %w", err)
		}
		c.hookManager.OnAfterRemove(ctx, file)
	}
	return nil
}

func (c *Controller) WriteIgnitionFiles(ctx context.Context, files []ignv3types.File) error {
	for _, file := range files {
		managedFile, err := c.deviceWriter.CreateManagedFile(file)
		if err != nil {
			return err
		}
		upToDate, err := managedFile.IsUpToDate()
		if err != nil {
			return err
		}
		if upToDate {
			continue
		}
		exists, err := managedFile.Exists()
		if err != nil {
			return err
		}
		if !exists {
			c.hookManager.OnBeforeCreate(ctx, file.Path)
		} else {
			c.hookManager.OnBeforeUpdate(ctx, file.Path)
		}
		if err := managedFile.Write(); err != nil {
			c.log.Warnf("failed to write file %s: %v", file.Path, err)
			// in order to create clearer error in status in case we fail in temp file creation
			// we don't want to return temp filename but rather change the error message to return given file path
			var err2 *fs.PathError
			if errors.As(err, &err2) {
				return fmt.Errorf("failed to write file %s: %w", file.Path, err2.Err)
			}
			return err
		}
		if !exists {
			c.hookManager.OnAfterCreate(ctx, file.Path)
		} else {
			c.hookManager.OnAfterUpdate(ctx, file.Path)
		}
	}
	return nil
}

func getFilePaths(currentFileList []ignv3types.File) []string {
	result := make([]string, len(currentFileList))
	for i, f := range currentFileList {
		result[i] = f.Path
	}
	return result
}
