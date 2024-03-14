package config

import (
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

// Config controller is responsible for ensuring the device configuration is reconciled
// against the device spec.
type Controller struct {
	deviceWriter  *fileio.Writer
	enrollmentCSR []byte

	log       *logrus.Logger
	logPrefix string
}

// NewController creates a new config controller.
func NewController(
	deviceWriter *fileio.Writer,
	enrollmentCSR []byte,
	log *logrus.Logger,
	logPrefix string,
) *Controller {
	return &Controller{
		enrollmentCSR: enrollmentCSR,
		deviceWriter:  deviceWriter,
		log:           log,
		logPrefix:     logPrefix,
	}
}

func (c *Controller) Sync(desired *v1alpha1.RenderedDeviceSpec) error {
	klog.V(4).Infof("%s syncing device configuration", c.logPrefix)
	defer klog.V(4).Infof("%s finished syncing device configuration", c.logPrefix)

	return c.ensureConfig(desired)
}

func (c *Controller) ensureConfig(desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Config == nil {
		klog.V(4).Infof("%s device config is nil", c.logPrefix)
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
