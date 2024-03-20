package device

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/image"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/sirupsen/logrus"
	"k8s.io/klog/v2"
)

const (
	RebootingReason       = "Rebooting"
	OsImageDegradedReason = "OSImageControllerDegraded"
)

type OSImageController struct {
	bootc         *image.BootcCmd
	statusManager status.Manager
	log           *logrus.Logger
	logPrefix     string
}

func NewOSImageController(
	executor executer.Executer,
	statusManager status.Manager,
	log *logrus.Logger,
	logPrefix string,
) *OSImageController {
	return &OSImageController{
		bootc:         image.NewBootcCmd(executor),
		statusManager: statusManager,
		log:           log,
		logPrefix:     logPrefix,
	}
}

func (c *OSImageController) Sync(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	klog.V(4).Infof("%s syncing device image", c.logPrefix)
	defer klog.V(4).Infof("%s finished syncing device image", c.logPrefix)

	err := c.ensureImage(ctx, desired)
	if err != nil {
		if err := c.statusManager.UpdateConditionError(ctx, OsImageDegradedReason, err); err != nil {
			klog.Errorf("Failed to update condition: %v", err)
		}
		return err
	}

	return nil
}

func (c *OSImageController) ensureImage(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Os == nil {
		klog.V(4).Infof("%s device os image is nil", c.logPrefix)
		return nil
	}

	host, err := c.bootc.Status(ctx)
	if err != nil {
		return err
	}

	// TODO: handle the case where the host is reconciled but also in a dirty state (staged).
	if image.IsOsImageReconciled(host, desired) {
		klog.V(4).Infof("Host is reconciled to os image %s", desired.Os.Image)
		return nil
	}

	image := desired.Os.Image
	c.log.Infof("Switching to os image: %s", image)
	if err := c.bootc.Switch(ctx, image); err != nil {
		return err
	}

	// Update the status to progressing
	if err := c.statusManager.UpdateCondition(ctx, v1alpha1.DeviceProgressing, v1alpha1.ConditionStatusTrue, RebootingReason, fmt.Sprintf("Rebooting into new os image: %s", image)); err != nil {
		return err
	}

	c.log.Infof("Os image switch complete - rebooting into new image")
	return c.bootc.Apply(ctx)
}
