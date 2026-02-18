package config

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	deviceerrors "github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	deviceWriter fileio.Writer
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	deviceWriter fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		deviceWriter: deviceWriter,
		log:          log,
	}
}

func (c *Controller) Sync(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	desiredFiles, err := ProviderSpecToFiles(desired.Config)
	if err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrConvertDesiredConfigToFiles, err)
	}

	currentFiles, err := ProviderSpecToFiles(current.Config)
	if err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrConvertCurrentConfigToFiles, err)
	}

	return c.ensureConfigFiles(currentFiles, desiredFiles)
}

func computeRemoval(currentFileList, desiredFileList []v1beta1.FileSpec) []string {
	desiredFiles := getFilePaths(desiredFileList)
	result := []string{}
	desiredMap := make(map[string]bool)

	for _, file := range desiredFiles {
		desiredMap[file] = true
	}

	currentFiles := getFilePaths(currentFileList)
	for _, file := range currentFiles {
		if !desiredMap[file] && len(file) > 0 {
			result = append(result, file)
		}
	}

	return result
}

func (c *Controller) ensureConfigFiles(currentFiles, desiredFiles []v1beta1.FileSpec) error {
	if err := c.removeObsoleteFiles(currentFiles, desiredFiles); err != nil {
		return fmt.Errorf("%w: %w", deviceerrors.ErrFailedToRemoveObsoleteFiles, err)
	}

	if len(desiredFiles) == 0 {
		c.log.Debug("No config files to write")
		// no files to write
		return nil
	}

	if err := c.writeFiles(desiredFiles); err != nil {
		c.log.Warnf("Writing config files failed: %+v", err)
		return fmt.Errorf("failed to apply configuration: %w", err)
	}
	return nil
}

// removeObsoleteFiles removes files that are present in the currentFiles but not in the desiredFiles.
func (c *Controller) removeObsoleteFiles(currentFiles, desiredFiles []v1beta1.FileSpec) error {
	removeFiles := computeRemoval(currentFiles, desiredFiles)
	for _, file := range removeFiles {
		if len(file) == 0 {
			continue
		}
		c.log.Debugf("Deleting file: %s", file)
		if err := c.deviceWriter.RemoveFile(file); err != nil {
			return fmt.Errorf("%w: %w", deviceerrors.ErrDeletingFilesFailed, err)
		}
	}
	return nil
}

func (c *Controller) writeFiles(files []v1beta1.FileSpec) error {
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
		if _, err = managedFile.Exists(); err != nil {
			return err
		}
		if err := managedFile.Write(); err != nil {
			c.log.Warnf("Failed to write file %s: %v", file.Path, err)
			// in order to create clearer error in status in case we fail in temp file creation
			// we don't want to return temp filename but rather change the error message to return given file path
			var pathErr *fs.PathError
			if errors.As(err, &pathErr) {
				return fmt.Errorf("write file %s: %w", file.Path, pathErr.Err)
			}
			return err
		}
	}
	return nil
}

func getFilePaths(currentFileList []v1beta1.FileSpec) []string {
	result := make([]string, len(currentFileList))
	for i, f := range currentFileList {
		result[i] = f.Path
	}
	return result
}

// ProviderSpecToFiles converts a list of ConfigProviderSpecs to a list of FileSpecs.
func ProviderSpecToFiles(configs *[]v1beta1.ConfigProviderSpec) ([]v1beta1.FileSpec, error) {
	if configs == nil || len(*configs) == 0 {
		return []v1beta1.FileSpec{}, nil
	}

	configItem := (*configs)[0]
	desiredProvider, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to convert config to inline config: %w", err)
	}

	return desiredProvider.Inline, nil
}

func FilesToProviderSpec(files []v1beta1.FileSpec) (*[]v1beta1.ConfigProviderSpec, error) {
	var provider v1beta1.ConfigProviderSpec
	err := provider.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{
		Inline: files,
	})
	if err != nil {
		return nil, fmt.Errorf("inline config: %w", err)
	}
	return &[]v1beta1.ConfigProviderSpec{provider}, nil
}
