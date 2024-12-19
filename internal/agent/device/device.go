package device

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
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
	systemdManager         systemd.Manager
	osManager              os.Manager
	policyManager          policy.Manager
	applicationsController *applications.Controller
	configController       *config.Controller
	resourceController     *resource.Controller
	consoleController      *console.ConsoleController
	bootcClient            container.BootcClient
	podmanClient           *client.Podman

	fetchSpecInterval   util.Duration
	fetchStatusInterval util.Duration

	once     sync.Once
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
	systemdManager systemd.Manager,
	fetchSpecInterval util.Duration,
	fetchStatusInterval util.Duration,
	hookManager hook.Manager,
	osManager os.Manager,
	policyManager policy.Manager,
	applicationsController *applications.Controller,
	configController *config.Controller,
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
		osManager:              osManager,
		policyManager:          policyManager,
		appManager:             appManager,
		systemdManager:         systemdManager,
		fetchSpecInterval:      fetchSpecInterval,
		fetchStatusInterval:    fetchStatusInterval,
		applicationsController: applicationsController,
		configController:       configController,
		resourceController:     resourceController,
		consoleController:      consoleController,
		bootcClient:            bootcClient,
		podmanClient:           podmanClient,
		cancelFn:               func() {},
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
	a.once.Do(func() {
		if a.cancelFn != nil {
			a.cancelFn()
		}
	})
	return nil
}

func (a *Agent) sync(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	if err := a.specManager.CheckPolicy(ctx, policy.Download, desired.RenderedVersion); err != nil {
		return fmt.Errorf("download policy: %w", err)
	}

	// the agent is validating the desired device spec and downloading
	// dependencies. no changes have been made to the device's configuration
	// yet.
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
			Type:    v1alpha1.DeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStatePreparing),
			Message: fmt.Sprintf("The device is preparing an update to renderedVersion: %s", desired.RenderedVersion),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	if err := a.beforeUpdate(ctx, current, desired); err != nil {
		return fmt.Errorf("before update: %w", err)
	}

	if err := a.specManager.CheckPolicy(ctx, policy.Update, desired.RenderedVersion); err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	// the agent has validated the desired spec, downloaded all dependencies,
	// and is ready to update. No changes have been made to the device's
	// configuration yet.
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
			Type:    v1alpha1.DeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStateReadyToUpdate),
			Message: fmt.Sprintf("The device is ready to apply update to renderedVersion: %s", desired.RenderedVersion),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	if err := a.syncDevice(ctx, current, desired); err != nil {
		// TODO: enable rollback on failure
		return fmt.Errorf("sync device: %w", err)
	}

	if err := a.afterUpdate(ctx, current, desired); err != nil {
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
		// if context is canceled return to exit the sync loop
		if errors.Is(err, context.Canceled) {
			return
		}
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

	if err := a.sync(ctx, current, desired); err != nil {
		return err
	}

	// reconciliation is a success, upgrade the current spec
	if err := a.specManager.Upgrade(ctx); err != nil {
		return err
	}

	if err := a.updatedStatus(ctx, desired); err != nil {
		a.log.Warnf("Failed updating status: %v", err)
	}

	return nil
}

func (a *Agent) updatedStatus(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusFalse,
		Reason:  string(v1alpha1.UpdateStateUpdated),
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

		updateFns = append(updateFns, status.SetOSImage(v1alpha1.DeviceOsStatus{
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
	if a.specManager.IsOSUpdate() {
		if err := a.osManager.BeforeUpdate(ctx, current, desired); err != nil {
			return fmt.Errorf("os: %w", err)
		}
	}

	if err := a.beforeUpdateApplications(ctx, current, desired); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	if err := a.hookManager.OnBeforeUpdating(ctx, current, desired); err != nil {
		return fmt.Errorf("hooks: %w", err)
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
		// pull the image if it does not exist. it is possible that the image
		// tag such as latest in which case it will be pulled later. but we
		// don't want to require calling out the network on every sync.
		if a.podmanClient.ImageExists(ctx, imageProvider.Image) {
			a.log.Debugf("Image %q already exists", imageProvider.Image)
			continue
		}

		providerImage := imageProvider.Image
		resp, err := a.podmanClient.Pull(ctx, providerImage, client.WithRetry())
		if err != nil {
			a.log.Warnf("Failed to pull image %q: %v", providerImage, err)
			return err
		}
		a.log.Debugf("Pulled image %q: %s", providerImage, resp)

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
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
			Type:    v1alpha1.DeviceUpdating,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  string(v1alpha1.UpdateStateApplyingUpdate),
			Message: fmt.Sprintf("The device is applying renderedVersion: %s", desired.RenderedVersion),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

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

	if err := a.systemdControllerSync(ctx, desired); err != nil {
		return fmt.Errorf("systemd: %w", err)
	}

	if err := a.policyManager.Sync(ctx, desired); err != nil {
		return fmt.Errorf("policy: %w", err)
	}

	return nil
}

func (a *Agent) systemdControllerSync(_ context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	var matchPatterns []string
	if desired.Systemd != nil {
		matchPatterns = util.FromPtr(desired.Systemd.MatchPatterns)
	}

	if err := a.systemdManager.EnsurePatterns(matchPatterns); err != nil {
		return err
	}

	return nil
}

func (a *Agent) afterUpdate(ctx context.Context, current, desired *v1alpha1.RenderedDeviceSpec) error {
	a.log.Debug("Executing after update actions")
	defer a.log.Debug("Finished executing after update actions")

	_, isOSReconciled, err := a.specManager.CheckOsReconciliation(ctx)
	if err != nil {
		return err
	}

	// execute after update for os first so that the activation of the spec is
	// after the os is updated.This happens because the os update requires a
	// reboot so the lower blocks are not executed until after reboot.
	if !isOSReconciled && a.specManager.IsOSUpdate() {
		if err = a.afterUpdateOS(ctx, desired); err != nil {
			a.log.Errorf("Error executing OS: %v", err)
			return err
		}
		return nil
	}

	// execute after update hooks. in the new OS image case, these will fire after the reboot
	// TODO: handle rebooted
	rebooted := false
	if err := a.hookManager.OnAfterUpdating(ctx, current, desired, rebooted); err != nil {
		a.log.Errorf("Error executing AfterUpdating hook: %v", err)
		return err
	}

	// execute after update for applications
	if err := a.appManager.AfterUpdate(ctx); err != nil {
		a.log.Errorf("Error executing actions: %v", err)
		return err
	}

	return nil
}

func (a *Agent) afterUpdateOS(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	if desired.Os == nil {
		a.log.Debug("No OS image to update")
		return nil
	}

	// switch to the new os image
	if err := a.osManager.AfterUpdate(ctx, desired); err != nil {
		a.log.Errorf("Error executing OS: %v", err)
		return err
	}

	// execute before rebooting hooks
	if err := a.hookManager.OnBeforeRebooting(ctx); err != nil {
		a.log.Errorf("Error executing BeforeRebooting hook: %v", err)
		return err
	}

	// update the rollback spec to reflect upgrade in progress
	if err := a.specManager.CreateRollback(ctx); err != nil {
		return err
	}

	image := desired.Os.Image
	infoMsg := fmt.Sprintf("Device is rebooting into os image: %s", image)
	_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusRebooting,
		Info:   util.StrToPtr(infoMsg),
	}))
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateErr = a.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.DeviceUpdating,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.UpdateStateRebooting),
		Message: infoMsg,
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return a.osManager.Reboot(ctx, desired)
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

		conditionUpdate.Reason = string(v1alpha1.UpdateStateError)
		conditionUpdate.Message = fmt.Sprintf("Failed to update to renderedVersion: %s", version)
		conditionUpdate.Status = v1alpha1.ConditionStatusFalse

		a.specManager.SetUpgradeFailed()
	} else {
		statusUpdate.Status = v1alpha1.DeviceSummaryStatusDegraded
		statusUpdate.Info = util.StrToPtr(fmt.Sprintf("Failed to sync device: %v", syncErr))

		conditionUpdate.Reason = string(v1alpha1.UpdateStateApplyingUpdate)
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
