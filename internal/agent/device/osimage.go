package device

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	OsImageDegradedReason     = "OSImageControllerDegraded"
	BootedWithUnexpectedImage = "BootedWithUnexpectedImage"
)

type OSImageController struct {
	bootc         client.Bootc
	statusManager status.Manager
	specManager   spec.Manager
	log           *log.PrefixLogger
}

func NewOSImageController(
	bootc client.Bootc,
	statusManager status.Manager,
	specManager spec.Manager,
	log *log.PrefixLogger,
) *OSImageController {
	return &OSImageController{
		bootc:         bootc,
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
	reconciled, err := container.IsOsImageReconciled(host, desired)
	if err != nil {
		return err
	}
	if reconciled {
		c.log.Debugf("Host is reconciled to os image %s", desired.Os.Image)
		return nil
	}

	image := desired.Os.Image
	c.log.Infof("Switching to os image: %s", image)
	if err := c.bootc.Switch(ctx, image); err != nil {
		return err
	}

	infoMsg := fmt.Sprintf("Device is rebooting into os image: %s", image)
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
		Reason:  string(v1alpha1.UpdateStateRebooting),
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

type OSManager interface {
	status.Exporter
}

func NewOSManager(bootcClient container.BootcClient) OSManager {
	return &osManager{
		bootcClient: bootcClient,
	}
}

type osManager struct {
	bootcClient container.BootcClient
}

func (m *osManager) Status(ctx context.Context, status *v1alpha1.DeviceStatus) error {
	bootcInfo, err := m.bootcClient.Status(ctx)
	if err != nil {
		return fmt.Errorf("getting bootc status: %w", err)
	}

	osImage := bootcInfo.GetBootedImage()
	if osImage == "" {
		return fmt.Errorf("getting booted os image: %w", err)
	}

	status.Os.Image = osImage
	status.Os.ImageDigest = bootcInfo.GetBootedImageDigest()
	return nil
}
