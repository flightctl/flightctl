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
	log          *log.PrefixLogger
}

// NewController creates a new config controller.
func NewController(
	deviceWriter *fileio.Writer,
	log *log.PrefixLogger,
) *Controller {
	return &Controller{
		deviceWriter: deviceWriter,
		log:          log,
	}
}

func (c *Controller) Sync(desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device configuration")
	defer c.log.Debug("Finished syncing device configuration")

	if desired.Config == nil {
		c.log.Debug("Device config is nil")
		return nil
	}

	desiredConfigRaw := []byte(*desired.Config)
	ignitionConfig, err := ParseAndConvertConfig(desiredConfigRaw)
	if err != nil {
		return fmt.Errorf("parsing and converting config failed: %w", err)
	}

	err = c.deviceWriter.WriteIgnitionFiles(ignitionConfig.Storage.Files...)
	if err != nil {
		return fmt.Errorf("writing ignition files failed: %w", err)
	}

	return nil
}
