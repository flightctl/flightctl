package device

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/bootimage"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	RebootingReason           = "Rebooting"
	OsImageDegradedReason     = "OSImageControllerDegraded"
	BootedWithUnexpectedImage = "BootedWithUnexpectedImage"
)

type OSImageController struct {
	imageManager  bootimage.Manager
	statusManager status.Manager
	log           *log.PrefixLogger
}

func NewOSImageController(
	executer executer.Executer,
	statusManager status.Manager,
	imageManager bootimage.Manager,
	log *log.PrefixLogger,
) *OSImageController {
	return &OSImageController{
		imageManager:  imageManager,
		statusManager: statusManager,
		log:           log,
	}
}

func (c *OSImageController) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Os == nil {
		c.log.Debugf("Device os image is nil")
		return nil
	}

	if c.imageManager.IsDisabled() {
		c.log.Warnf("Image manager is disabled: can not reconcile image: %s", desired.Os.Image)
		return nil
	}

	c.log.Debug("Syncing device image")
	defer c.log.Debug("Finished syncing device image")

	err := c.ensureImage(ctx, desired)
	if err != nil {
		if updateErr := c.statusManager.UpdateConditionError(ctx, OsImageDegradedReason, err); updateErr != nil {
			c.log.Errorf("Failed to update condition: %v", updateErr)
		}
		return err
	}

	return nil
}

func (c *OSImageController) ensureImage(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {

	imageStatus, err := c.imageManager.Status(ctx)
	if err != nil {
		return err
	}
	// TODO: handle the case where the host is reconciled but also in a dirty state (staged).
	if imageStatus.IsBootedImageExpected(desired) {
		c.log.Debugf("Host is reconciled to os image %s", desired.Os.Image)
		return nil
	}

	image := desired.Os.Image
	c.log.Infof("Switching to os image: %s", image)
	if err := c.imageManager.Switch(ctx, image); err != nil {
		return err
	}

	// Update the status to progressing
	if err := c.statusManager.UpdateCondition(ctx, v1alpha1.DeviceProgressing, v1alpha1.ConditionStatusTrue, RebootingReason, fmt.Sprintf("Rebooting into new os image: %s", image)); err != nil {
		return err
	}

	c.log.Infof("Os image switch complete - rebooting into new image")
	return c.imageManager.Apply(ctx)
}
