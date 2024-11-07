package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/lthibault/jitterbug"
	"k8s.io/apimachinery/pkg/util/wait"
)

// Agent is responsible for managing the applications, configuration and status of the device.
type Agent struct {
	name                   string
	deviceWriter           fileio.Writer
	statusManager          status.Manager
	specManager            spec.Manager
	hookManager            hook.Manager
	appManager             applications.Manager
	applicationsController *applications.Controller
	configController       *config.Controller
	osImageController      *OSImageController
	resourceController     *resource.Controller
	consoleController      *console.ConsoleController
	bootcClient            container.BootcClient
	podmanClient           *client.Podman

	fetchSpecInterval   util.Duration
	fetchStatusInterval util.Duration

	cancelFn context.CancelFunc
	backoff  wait.Backoff
	log      *log.PrefixLogger
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	deviceWriter fileio.Writer,
	statusManager status.Manager,
	specManager spec.Manager,
	appManager applications.Manager,
	fetchSpecInterval util.Duration,
	fetchStatusInterval util.Duration,
	hookManager hook.Manager,
	applicationsController *applications.Controller,
	configController *config.Controller,
	osImageController *OSImageController,
	resourceController *resource.Controller,
	consoleController *console.ConsoleController,
	bootcClient container.BootcClient,
	podmanClient *client.Podman,
	backoff wait.Backoff,
	log *log.PrefixLogger,
) *Agent {
	return &Agent{
		name:                   name,
		deviceWriter:           deviceWriter,
		statusManager:          statusManager,
		specManager:            specManager,
		hookManager:            hookManager,
		appManager:             appManager,
		fetchSpecInterval:      fetchSpecInterval,
		fetchStatusInterval:    fetchStatusInterval,
		applicationsController: applicationsController,
		configController:       configController,
		osImageController:      osImageController,
		resourceController:     resourceController,
		consoleController:      consoleController,
		bootcClient:            bootcClient,
		podmanClient:           podmanClient,
		backoff:                backoff,
		log:                    log,
	}
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	ctx, a.cancelFn = context.WithCancel(ctx)

	specTicker := jitterbug.New(time.Duration(a.fetchSpecInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer specTicker.Stop()
	statusTicker := jitterbug.New(time.Duration(a.fetchStatusInterval), &jitterbug.Norm{Stdev: 30 * time.Millisecond, Mean: 0})
	defer statusTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-specTicker.C:
			a.syncSpec(ctx, a.syncSpecFn)
		case <-statusTicker.C:
			a.pushStatus(ctx)
		}
	}
}

// Stop ensures that the device agent stops reconciling during graceful shutdown.
func (a *Agent) Stop(ctx context.Context) error {
	a.cancelFn()
	return nil
}

func (a *Agent) sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.beforeUpdate(ctx, current, desired); err != nil {
		return fmt.Errorf("before update: %w", err)
	}

	if err := a.syncDevice(ctx, current, desired); err != nil {
		// TODO: enable rollback on failure
		return fmt.Errorf("sync device: %w", err)
	}

	if err := a.afterUpdate(ctx); err != nil {
		return fmt.Errorf("after update: %w", err)
	}

	return nil
}

func (a *Agent) syncSpec(ctx context.Context, syncFn func(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error) {
	startTime := time.Now()
	a.log.Debug("Starting sync of device spec")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed sync of device spec in %v", duration)
	}()

	desired, requeue, err := a.specManager.GetDesired(ctx)
	if err != nil {
		a.log.Errorf("Failed to get desired spec: %v", err)
		return
	}
	if requeue {
		a.log.Debug("Requeueing spec")
		return
	}

	if err := syncFn(ctx, desired); err != nil {
		a.handleSyncError(ctx, desired, err)
		return
	}

	_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusOnline,
		Info:   nil,
	}))
	if updateErr != nil {
		a.log.Errorf("Updating device status: %v", updateErr)
	}
}

func (a *Agent) syncSpecFn(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	current, err := a.specManager.Read(spec.Current)
	if err != nil {
		return err
	}

	if current.RenderedVersion == "" && desired.RenderedVersion == "" {
		return nil
	}

	if spec.IsUpgrading(current, desired) {
		updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
			Type:    v1alpha1.DeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  "Update",
			Message: fmt.Sprintf("The device is upgrading to renderedVersion: %s", desired.RenderedVersion),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	if err := a.sync(ctx, current, desired); err != nil {
		return err
	}

	if err := a.specManager.Upgrade(); err != nil {
		return err
	}

	return a.upgradeStatus(ctx, desired)
}

func (a *Agent) upgradeStatus(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusFalse,
		Reason:  "Updated",
		Message: fmt.Sprintf("Updated to desired renderedVersion: %s", desired.RenderedVersion),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateFns := []status.UpdateStatusFn{
		status.SetConfig(v1alpha1.DeviceConfigStatus{
			RenderedVersion: desired.RenderedVersion,
		}),
	}

	if desired.Os != nil {
		bootcStatus, err := a.bootcClient.Status(ctx)
		if err != nil {
			return err
		}

		updateFns = append(updateFns, status.SetOSImage(v1alpha1.DeviceOSStatus{
			Image:       desired.Os.Image,
			ImageDigest: bootcStatus.GetBootedImageDigest(),
		}))
	}

	_, updateErr = a.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (a *Agent) pushStatus(ctx context.Context) {
	startTime := time.Now()
	a.log.Debug("Started collecting device status")
	defer func() {
		duration := time.Since(startTime)
		a.log.Debugf("Completed pushing device status in: %v", duration)
	}()

	if err := a.statusManager.Sync(ctx); err != nil {
		msg := err.Error()
		_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusDegraded,
			Info:   &msg,
		}))
		if updateErr != nil {
			a.log.Errorf("Updating device status: %v", updateErr)
		}
		a.log.Errorf("Syncing status: %v", err)
	}
}

func (a *Agent) beforeUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.beforeUpdateApplications(ctx, current, desired); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	return nil
}

func (a *Agent) beforeUpdateApplications(ctx context.Context, _, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Applications == nil {
		a.log.Debug("No applications to pre-check")
		return nil
	}
	a.log.Debug("Pre-checking applications dependencies")
	defer a.log.Debug("Finished pre-checking application dependencies")

	// ensure dependencies for image based application manifests
	imageProviders, err := applications.ImageProvidersFromSpec(desired)
	if err != nil {
		return fmt.Errorf("%w: parsing image providers: %w", errors.ErrNoRetry, err)
	}

	for _, imageProvider := range imageProviders {
		if a.podmanClient.ImageExists(ctx, imageProvider.Image) {
			a.log.Debugf("Image %q already exists", imageProvider.Image)
			continue
		}

		providerImage := imageProvider.Image
		// pull the image if it does not exist. it is possible that the image
		// tag such as latest in which case it will be pulled later. but we
		// don't want to require calling out the network on every sync.
		if !a.podmanClient.ImageExists(ctx, imageProvider.Image) {
			err := wait.ExponentialBackoffWithContext(ctx, a.backoff, func() (bool, error) {
				resp, err := a.podmanClient.Pull(ctx, providerImage)
				if err != nil {
					a.log.Warnf("Failed to pull image %q: %v", providerImage, err)
					return false, nil
				}
				a.log.Debugf("Pulled image %q: %s", providerImage, resp)
				return true, nil
			})
			if err != nil {
				return fmt.Errorf("pulling image: %w", err)
			}
		}

		addType, err := applications.TypeFromImage(ctx, a.podmanClient, providerImage)
		if err != nil {
			return fmt.Errorf("%w: getting application type: %w", errors.ErrNoRetry, err)
		}
		if err := applications.EnsureDependenciesFromType(addType); err != nil {
			return fmt.Errorf("%w: ensuring dependencies: %w", errors.ErrNoRetry, err)
		}
	}

	return nil
}

func (a *Agent) syncDevice(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.consoleController.Sync(ctx, desired); err != nil {
		a.log.Errorf("Failed to sync console configuration: %s", err)
	}

	if err := a.applicationsController.Sync(ctx, current, desired); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	if err := a.hookManager.Sync(current, desired); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}

	if err := a.configController.Sync(ctx, current, desired); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := a.resourceController.Sync(ctx, desired); err != nil {
		return fmt.Errorf("resources: %w", err)
	}

	if err := a.osImageController.Sync(ctx, desired); err != nil {
		return fmt.Errorf("os image: %w", err)
	}

	// set status collector properties based on new desired spec
	a.statusManager.SetProperties(desired)

	return nil
}

func (a *Agent) afterUpdate(ctx context.Context) error {
	a.log.Debug("Executing post actions")
	defer a.log.Debug("Finished executing post actions")

	// execute post actions for applications
	if err := a.appManager.ExecuteActions(ctx); err != nil {
		a.log.Errorf("Error executing actions: %v", err)
		return err
	}

	return nil
}

func (a *Agent) handleSyncError(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec, syncErr error) {
	version := desired.RenderedVersion
	statusUpdate := v1alpha1.DeviceSummaryStatus{}
	conditionUpdate := v1alpha1.Condition{
		Type: v1alpha1.DeviceUpdating,
	}

	if !errors.IsRetryable(syncErr) {
		a.log.Errorf("Marking template version %v as failed: %v", version, syncErr)

		statusUpdate.Status = v1alpha1.DeviceSummaryStatusError
		statusUpdate.Info = util.StrToPtr(fmt.Sprintf("Reconciliation failed for version %v: %v", version, syncErr))

		conditionUpdate.Reason = "Failed"
		conditionUpdate.Message = fmt.Sprintf("Failed to update to renderedVersion: %s", version)
		conditionUpdate.Status = v1alpha1.ConditionStatusFalse

		a.specManager.SetUpgradeFailed()
	} else {
		statusUpdate.Status = v1alpha1.DeviceSummaryStatusDegraded
		statusUpdate.Info = util.StrToPtr(fmt.Sprintf("Failed to sync device: %v", syncErr))

		conditionUpdate.Reason = "Retry"
		conditionUpdate.Message = fmt.Sprintf("Failed to update to renderedVersion: %s. Retrying", version)
		conditionUpdate.Status = v1alpha1.ConditionStatusTrue
	}

	if _, err := a.statusManager.Update(ctx, status.SetDeviceSummary(statusUpdate)); err != nil {
		a.log.Errorf("Failed to update device status: %v", err)
	}

	if err := a.statusManager.UpdateCondition(ctx, conditionUpdate); err != nil {
		a.log.Warnf("Failed to update device status condition: %v", err)
	}

	a.log.Error(*statusUpdate.Info)
}
