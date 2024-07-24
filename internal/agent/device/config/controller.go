package config

import (
	"errors"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/pkg/log"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	hookManager  hook.Manager
	deviceWriter *fileio.Writer
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	hookManager hook.Manager,
	deviceWriter *fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		hookManager:  hookManager,
		deviceWriter: deviceWriter,
		log:          log,
	}
}

func (c *Controller) Sync(desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	// post hooks
	if desired.Hooks.Pre == nil {
		c.log.Debug("No pre hooks defined in desired spec")
		// Reset all post config hooks to default
		if err := c.hookManager.Config().Pre().Reset(); err != nil {
			return err
		}
	} else {
		if err := c.ensurePreHooks(desired.Hooks.Pre); err != nil {
			return err
		}
	}

	// pre hooks
	if desired.Hooks.Post == nil {
		c.log.Debug("No post hooks defined in desired spec")
		// Reset all post config hooks to default
		if err := c.hookManager.Config().Post().Reset(); err != nil {
			return err
		}
	} else {
		// order is important here install new hooks before applying config data
		// so they can be consumed.
		if err := c.ensurePostHooks(desired.Hooks.Post); err != nil {
			return err
		}
	}

	// config
	if desired.Config != nil {
		data := *desired.Config
		return c.ensureConfigData(data)
	}

	return nil
}

func (c *Controller) ensureConfigData(data string) error {
	desiredConfigRaw := []byte(data)
	ignitionConfig, err := ParseAndConvertConfig(desiredConfigRaw)
	if err != nil {
		return fmt.Errorf("parsing and converting config failed: %w", err)
	}

	// calculate diff between existing and desired files
	removeFiles := c.hookManager.Config().Pre().ComputeRemoval(ignitionConfig.Storage.Files)
	for _, file := range removeFiles {
		c.log.Infof("Deleting file: %s", file)
		// trigger delete pre hook and wait for it to complete
		if err := c.hookManager.Config().Pre().Remove(file); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				c.log.Warnf("File %s does not exist skipping removal", file)
				continue
			}
		}

		if err := c.deviceWriter.RemoveFile(file); err != nil {
			return fmt.Errorf("deleting files failed: %w", err)
		}
	}

	// write ignition files to disk and trigger pre hooks
	err = c.deviceWriter.WriteIgnitionFiles(ignitionConfig.Storage.Files, c.hookManager.Config().Pre().CreateOrUpdate)
	if err != nil {
		return fmt.Errorf("writing ignition files failed: %w", err)
	}
	return nil
}

func (c *Controller) ensurePostHooks(hooks *[]v1alpha1.DeviceHookSpec) error {
	newWatchPaths := make(map[string]struct{})
	for i := range *hooks {
		hook := (*hooks)[i]
		newWatchPaths[hook.Path] = struct{}{}
		updated, err := c.hookManager.Config().Post().Update(&hook)
		if err != nil {
			return err
		}
		if updated {
			c.log.Infof("Updated hook: %s", hook.Path)
		}
	}

	// remove stale watches
	existingWatchPaths := c.hookManager.Config().Post().ListWatches()
	for _, existingWatchPath := range existingWatchPaths {
		if _, ok := newWatchPaths[existingWatchPath]; !ok {
			if err := c.hookManager.Config().Post().RemoveWatch(existingWatchPath); err != nil {
				return err
			}
			c.log.Infof("Removed watch: %s", existingWatchPath)
		}
	}

	return nil
}

func (c *Controller) ensurePreHooks(hooks *[]v1alpha1.DeviceHookSpec) error {
	newWatchPaths := make(map[string]struct{})
	for i := range *hooks {
		hook := (*hooks)[i]
		newWatchPaths[hook.Path] = struct{}{}
		updated, err := c.hookManager.Config().Pre().Update(&hook)
		if err != nil {
			return err
		}
		if updated {
			c.log.Infof("Updated hook: %s", hook.Path)
		}
	}

	// remove stale watches
	existingWatchPaths := c.hookManager.Config().Pre().ListWatches()
	for _, existingWatchPath := range existingWatchPaths {
		if _, ok := newWatchPaths[existingWatchPath]; !ok {
			if err := c.hookManager.Config().Pre().RemoveWatch(existingWatchPath); err != nil {
				return err
			}
			c.log.Infof("Removed watch: %s", existingWatchPath)
		}
	}

	return nil
}
