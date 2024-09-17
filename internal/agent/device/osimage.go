package device

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/image"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	RebootingReason           = "Rebooting"
	OsImageDegradedReason     = "OSImageControllerDegraded"
	BootedWithUnexpectedImage = "BootedWithUnexpectedImage"
)

type OSImageController struct {
	bootc         *container.BootcCmd
	statusManager status.Manager
	specManager   spec.Manager
	log           *log.PrefixLogger
}

func NewOSImageController(
	executer executer.Executer,
	statusManager status.Manager,
	specManager spec.Manager,
	log *log.PrefixLogger,
) *OSImageController {
	return &OSImageController{
		bootc:         container.NewBootcCmd(executer),
		statusManager: statusManager,
		specManager:   specManager,
		log:           log,
	}
}

func (c *OSImageController) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	c.log.Debug("Syncing device image")
	defer c.log.Debug("Finished syncing device image")

	err := c.ensureImage(ctx, desired)
	if err != nil {
		return fmt.Errorf("failed to update os image: %w", err)
	}

	return nil
}

func (c *OSImageController) ensureImage(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Os == nil {
		c.log.Debugf("Device os image is nil")
		return nil
	}

	host, err := c.bootc.Status(ctx)
	if err != nil {
		return err
	}

	// TODO: handle the case where the host is reconciled but also in a dirty state (staged).
	if IsOsImageReconciled(host, desired) {
		c.log.Debugf("Host is reconciled to os image %s", desired.Os.Image)
		return nil
	}

	desiredImage := image.SpecToImage(desired.Os)
	c.log.Infof("Switching to os image: %s", desiredImage)
	target := image.ImageToBootcTarget(desiredImage)
	if err := c.bootc.Switch(ctx, target); err != nil {
		return err
	}

	infoMsg := fmt.Sprintf("Device is rebooting into os image: %s", desiredImage)
	_, updateErr := c.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusRebooting,
		Info:   util.StrToPtr(infoMsg),
	}))
	if updateErr != nil {
		c.log.Warnf("Failed setting status: %v", err)
	}

	updateErr = c.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  "Rebooting",
		Message: infoMsg,
	})
	if updateErr != nil {
		c.log.Warnf("Failed setting status: %v", err)
	}

	c.log.Info(infoMsg)

	if err := c.specManager.PrepareRollback(ctx); err != nil {
		return err
	}

	return c.bootc.Apply(ctx)
}

// IsOsImageReconciled returns true if the booted image equals the spec image.
func IsOsImageReconciled(host *container.BootcHost, desiredSpec *v1alpha1.RenderedDeviceSpec) bool {
	if desiredSpec.Os == nil {
		return false
	}

	desiredImage := image.SpecToImage(desiredSpec.Os)
	bootedImage := image.BootcStatusToImage(host)

	// If the booted image equals the desired image, the OS image is reconciled
	return image.AreImagesEquivalent(desiredImage, bootedImage)
}
