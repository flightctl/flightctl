package config

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/log"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	deviceWriter *fileio.Writer
	trackedFiles map[string]struct{}
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	deviceWriter *fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		deviceWriter: deviceWriter,
		trackedFiles: make(map[string]struct{}),
		log:          log,
	}
}

func (c *Controller) Sync(current *v1alpha1.RenderedDeviceSpec, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	if desired.Config == nil {
		c.log.Debug("Device config is nil")
		return nil
	}

	desiredConfigRaw := []byte(*desired.Config)
	ignitionConfig, err := ParseAndConvertConfig(desiredConfigRaw)
	if err != nil {
		return err
	}

	// warm cache if empty
	if len(c.trackedFiles) == 0 && current.Config != nil {
		// initialize tracked files
		currentConfigRaw := []byte(*current.Config)
		currentIgnitionConfig, err := ParseAndConvertConfig(currentConfigRaw)
		if err != nil {
			return err
		}
		for _, file := range currentIgnitionConfig.Storage.Files {
			c.trackedFiles[file.Path] = struct{}{}
		}
	}

	// calculate diff between existing and desired files
	desiredIgnFiles := make(map[string]struct{})
	for _, file := range ignitionConfig.Storage.Files {
		desiredIgnFiles[file.Path] = struct{}{}
	}

	for _, file := range c.computeRemoval(desiredIgnFiles) {
		c.log.Infof("Deleting stale file no longer part of config: %s", file)
		if err := c.deviceWriter.RemoveFile(file); err != nil {
			return fmt.Errorf("deleting files failed: %w", err)
		}
	}

	err = c.deviceWriter.WriteIgnitionFiles(ignitionConfig.Storage.Files...)
	if err != nil {
		return fmt.Errorf("writing ignition files failed: %w", err)
	}

	c.trackedFiles = desiredIgnFiles

	return nil
}

func (c *Controller) computeRemoval(desiredIgnFiles map[string]struct{}) []string {
	var removeFilePaths []string
	for path := range c.trackedFiles {
		if _, exists := desiredIgnFiles[path]; !exists {
			removeFilePaths = append(removeFilePaths, path)
		}
	}

	return removeFilePaths
}
