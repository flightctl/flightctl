package device

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	agent_config "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/config"
	"github.com/flightctl/flightctl/internal/agent/device/console"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systemd"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
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
	lifecycleManager       lifecycle.Manager
	resourceManager        resource.Manager
	applicationsController *applications.Controller
	configController       *config.Controller
	consoleManager         *console.Manager
	osClient               os.Client
	podmanClient           *client.Podman
	prefetchManager        dependency.PrefetchManager

	statusUpdateInterval util.Duration

	backoff wait.Backoff
	log     *log.PrefixLogger
}

// NewAgent creates a new device agent.
func NewAgent(
	name string,
	deviceWriter fileio.Writer,
	statusManager status.Manager,
	specManager spec.Manager,
	appManager applications.Manager,
	systemdManager systemd.Manager,
	statusUpdateInterval util.Duration,
	hookManager hook.Manager,
	osManager os.Manager,
	policyManager policy.Manager,
	lifecycleManager lifecycle.Manager,
	applicationsController *applications.Controller,
	configController *config.Controller,
	resourceManager resource.Manager,
	consoleManager *console.Manager,
	osClient os.Client,
	podmanClient *client.Podman,
	prefetchManager dependency.PrefetchManager,
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
		lifecycleManager:       lifecycleManager,
		appManager:             appManager,
		systemdManager:         systemdManager,
		statusUpdateInterval:   statusUpdateInterval,
		applicationsController: applicationsController,
		configController:       configController,
		resourceManager:        resourceManager,
		consoleManager:         consoleManager,
		osClient:               osClient,
		podmanClient:           podmanClient,
		prefetchManager:        prefetchManager,
		backoff:                backoff,
		log:                    log,
	}
}

// Run starts the device agent reconciliation loop.
func (a *Agent) Run(ctx context.Context) error {
	// orchestrates periodic fetching of device specs and pushing status updates
	engine := NewEngine(
		a.syncDeviceSpec,
		a.statusUpdateInterval,
		a.statusUpdate,
	)

	return engine.Run(ctx)
}

func (a *Agent) sync(ctx context.Context, current, desired *v1beta1.Device) error {
	if !spec.IsRollback(current, desired) {
		if err := a.beforeUpdate(ctx, current, desired); err != nil {
			return fmt.Errorf("before update: %w", err)
		}
	}

	if err := a.syncDevice(ctx, current, desired); err != nil {
		return fmt.Errorf("sync device: %w", err)
	}

	if err := a.afterUpdate(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("after update: %w", err)
	}

	return nil
}

func (a *Agent) syncDeviceSpec(ctx context.Context) {
	startTime := time.Now()
	a.log.Trace("Starting sync of device spec")
	defer func() {
		duration := time.Since(startTime)
		a.log.Tracef("Completed sync of device spec in %v", duration)
	}()

	desired, requeue, err := a.specManager.GetDesired(ctx)
	if err != nil {
		a.log.Errorf("Failed to get desired spec: %v", err)
		return
	}
	if requeue {
		a.log.Trace("Requeueing spec")
		return
	}

	a.log.Debugf("Reconciling spec version %s", desired.Version())

	current, err := a.specManager.Read(spec.Current)
	if err != nil {
		a.log.Errorf("Failed to get current spec: %v", err)
		return
	}

	if syncErr := a.sync(ctx, current, desired); syncErr != nil {
		// if context is canceled return to exit the sync loop
		if errors.Is(syncErr, context.Canceled) {
			return
		}

		if !a.specManager.IsUpgrading() {
			// if the device is not upgrading, we should never see a sync error
			// because the device is in a steady state. this is a potential bug in
			// the reconciliation loop if the error is not retryable.
			if !errors.IsRetryable(syncErr) {
				a.log.Errorf("Steady state is no longer in sync: %v", syncErr)
			} else {
				a.log.Warnf("Retryable error observed during the steady state: %v", syncErr)
			}
			return
		}

		// non retryable error fails version
		if !errors.IsRetryable(syncErr) {
			a.log.Errorf("Marking template version %v as failed: %v", desired.Version(), syncErr)
			if err := a.specManager.SetUpgradeFailed(desired.Version()); err != nil {
				a.log.Errorf("Failed to set upgrade failed: %v", err)
			}
		}

		// handle prefetch not ready
		if errors.Is(syncErr, errors.ErrPrefetchNotReady) {
			a.handlePrefetchNotReady(ctx, syncErr)
			return
		}

		// Policy may defer update; in all cases, warn and roll back to the previous renderedVersion.
		if errors.Is(syncErr, errors.ErrUpdatePolicyNotReady) || errors.Is(syncErr, errors.ErrDownloadPolicyNotReady) {
			a.log.Warnf("Requeuing version %s: %s", current.Version(), syncErr.Error())
		} else {
			a.log.Warnf("Attempting to rollback to previous renderedVersion: %s", current.Version())
		}

		if err := a.rollbackDevice(ctx, current, desired, a.sync); err != nil {
			a.log.Errorf("Rollback did not complete cleanly: %v", err)
		}

		a.handleSyncError(ctx, desired, syncErr)
		return
	}

	defer a.prefetchManager.Cleanup()

	// skip status update if the device is in a steady state and not upgrading.
	// also ensures previous failed status is not overwritten.
	if !a.specManager.IsUpgrading() {
		a.log.Debug("No upgrade in progress, skipping status update")
		return
	}

	// reconciliation is a success, upgrade the current spec
	if err := a.specManager.Upgrade(ctx); err != nil {
		if errors.IsContext(err) {
			a.log.Debugf("Sync is shutting down : %v", err)
			return
		}
		a.log.Errorf("Failed to upgrade spec: %v", err)
		return
	}

	if err := a.updatedStatus(ctx, desired); err != nil {
		a.log.Warnf("Failed updating status: %v", err)
	}
}

func (a *Agent) rollbackDevice(ctx context.Context, current, desired *v1beta1.Device, syncFn func(context.Context, *v1beta1.Device, *v1beta1.Device) error) error {
	updateErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.UpdateStateRollingBack),
		Message: "Device is rolling back to the previous renderedVersion: " + current.Version(),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	if err := a.specManager.Rollback(ctx); err != nil {
		return err
	}

	// note: we are explicitly reversing the order of the current and desired to
	// ensure a clean rollback state.
	return syncFn(ctx, desired, current)
}

func (a *Agent) updatedStatus(ctx context.Context, desired *v1beta1.Device) error {
	updateErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusFalse,
		Reason:  string(v1beta1.UpdateStateUpdated),
		Message: fmt.Sprintf("Updated to desired renderedVersion: %s", desired.Version()),
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateFns := []status.UpdateStatusFn{
		status.SetConfig(v1beta1.DeviceConfigStatus{
			RenderedVersion: desired.Version(),
		}),
	}

	if desired.Spec.Os != nil {
		osStatus, err := a.osClient.Status(ctx)
		if err != nil {
			return err
		}

		updateFns = append(updateFns, status.SetOSImage(v1beta1.DeviceOsStatus{
			Image:       desired.Spec.Os.Image,
			ImageDigest: osStatus.GetBootedImageDigest(),
		}))
	}

	_, updateErr = a.statusManager.Update(ctx, updateFns...)
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (a *Agent) statusUpdate(ctx context.Context) {
	startTime := time.Now()
	a.log.Trace("Started collecting device status")
	defer func() {
		duration := time.Since(startTime)
		a.log.Tracef("Completed pushing device status in: %v", duration)
	}()

	if err := a.statusManager.Sync(ctx); err != nil {
		a.log.Errorf("Syncing status: %v", err)
	}
}

func (a *Agent) beforeUpdate(ctx context.Context, current, desired *v1beta1.Device) error {
	if err := a.resourceManager.BeforeUpdate(ctx, desired.Spec); err != nil {
		return fmt.Errorf("resources: %w", err)
	}

	if err := a.specManager.CheckPolicy(ctx, policy.Download, desired.Version()); err != nil {
		return fmt.Errorf("download policy: %w", err)
	}

	// the agent is validating the desired device spec and downloading
	// dependencies. no changes have been made to the device's configuration
	// yet.
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
			Type:    v1beta1.ConditionTypeDeviceUpdating,
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  string(v1beta1.UpdateStatePreparing),
			Message: fmt.Sprintf("Device is preparing an update to renderedVersion: %s", desired.Version()),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	a.prefetchManager.RegisterOCICollector(a.appManager)
	if a.specManager.IsOSUpdate() {
		a.prefetchManager.RegisterOCICollector(a.osManager)
	}

	if err := a.prefetchManager.BeforeUpdate(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("prefetch: %w", err)
	}

	if err := a.appManager.BeforeUpdate(ctx, desired.Spec); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	if err := a.hookManager.OnBeforeUpdating(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}

	if err := a.specManager.CheckPolicy(ctx, policy.Update, desired.Version()); err != nil {
		return fmt.Errorf("update policy: %w", err)
	}

	// the agent has validated the desired spec, downloaded all dependencies,
	// and is ready to update. no changes have been made to the device's
	// configuration yet.
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
			Type:    v1beta1.ConditionTypeDeviceUpdating,
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  string(v1beta1.UpdateStateReadyToUpdate),
			Message: fmt.Sprintf("Device is ready to apply update to renderedVersion: %s", desired.Version()),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	return nil
}

func (a *Agent) syncDevice(ctx context.Context, current, desired *v1beta1.Device) error {
	if a.specManager.IsUpgrading() {
		updateErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
			Type:    v1beta1.ConditionTypeDeviceUpdating,
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  string(v1beta1.UpdateStateApplyingUpdate),
			Message: fmt.Sprintf("Device is applying renderedVersion: %s", desired.Version()),
		})
		if updateErr != nil {
			a.log.Warnf("Failed setting status: %v", updateErr)
		}
	}

	if err := a.applicationsController.Sync(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("applications: %w", err)
	}

	if err := a.hookManager.Sync(current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("hooks: %w", err)
	}

	if err := a.configController.Sync(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := a.systemdControllerSync(ctx, desired.Spec); err != nil {
		return fmt.Errorf("systemd: %w", err)
	}

	if err := a.lifecycleManager.Sync(ctx, current.Spec, desired.Spec); err != nil {
		return fmt.Errorf("lifecycle: %w", err)
	}

	// NOTE: policy manager is reconciled early in sync() so that the agent
	// can correct for an invalid policy.

	return nil
}

func (a *Agent) systemdControllerSync(_ context.Context, desired *v1beta1.DeviceSpec) error {
	var matchPatterns []string
	if desired.Systemd != nil {
		matchPatterns = lo.FromPtr(desired.Systemd.MatchPatterns)
	}

	if err := a.systemdManager.EnsurePatterns(matchPatterns); err != nil {
		return err
	}

	return nil
}

func (a *Agent) afterUpdate(ctx context.Context, current, desired *v1beta1.DeviceSpec) error {
	a.log.Debug("Executing after update actions")
	defer a.log.Debug("Finished executing after update actions")

	// execute after update for lifecycle
	if err := a.lifecycleManager.AfterUpdate(ctx, current, desired); err != nil {
		a.log.Errorf("Error executing lifecycle: %v", err)
		return err
	}

	_, isOSReconciled, err := a.specManager.CheckOsReconciliation(ctx)
	if err != nil {
		a.log.Errorf("Error checking is os reconciled: %v", err)
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

func (a *Agent) afterUpdateOS(ctx context.Context, desired *v1beta1.DeviceSpec) error {
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
	_, updateErr := a.statusManager.Update(ctx, status.SetDeviceSummary(v1beta1.DeviceSummaryStatus{
		Status: v1beta1.DeviceSummaryStatusRebooting,
		Info:   lo.ToPtr(infoMsg),
	}))
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	updateErr = a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.UpdateStateRebooting),
		Message: infoMsg,
	})
	if updateErr != nil {
		a.log.Warnf("Failed setting status: %v", updateErr)
	}

	return a.osManager.Reboot(ctx, desired)
}

func (a *Agent) handleSyncError(ctx context.Context, desired *v1beta1.Device, syncErr error) {
	if syncErr == nil {
		return
	}

	version := desired.Version()
	conditionUpdate := v1beta1.Condition{
		Type: v1beta1.ConditionTypeDeviceUpdating,
	}

	if !errors.IsRetryable(syncErr) {
		msg := fmt.Sprintf("Failed to update to renderedVersion: %s: %v", version, syncErr.Error())
		conditionUpdate.Reason = string(v1beta1.UpdateStateError)
		conditionUpdate.Message = log.Truncate(msg, status.MaxMessageLength)
		conditionUpdate.Status = v1beta1.ConditionStatusFalse
		a.prefetchManager.Cleanup()
		a.log.Error(msg)
	} else {
		msg := fmt.Sprintf("Failed to update to renderedVersion: %s: retrying: %v", version, syncErr.Error())
		conditionUpdate.Reason = string(v1beta1.UpdateStateApplyingUpdate)
		conditionUpdate.Message = log.Truncate(msg, status.MaxMessageLength)
		conditionUpdate.Status = v1beta1.ConditionStatusTrue
		a.log.Warn(msg)
	}

	if err := a.statusManager.UpdateCondition(ctx, conditionUpdate); err != nil {
		a.log.Warnf("Failed to update device status condition: %v", err)
	}
}

func (a *Agent) handlePrefetchNotReady(ctx context.Context, syncErr error) {
	statusMsg := a.prefetchManager.StatusMessage(ctx)
	a.log.Debugf("Image prefetch in progress: %s", statusMsg)

	// rollback the spec to the previous version
	if err := a.specManager.Rollback(ctx); err != nil {
		a.log.Errorf("Failed to rollback spec: %v", err)
	}

	updateStatusErr := a.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.UpdateStatePreparing),
		Message: statusMsg,
	})
	if updateStatusErr != nil {
		a.log.Warnf("Failed to update status for image prefetch progress: %v", updateStatusErr)
	}
}

// ReloadConfig reloads the device agent configuration.
// This is called when the device agent is reloaded.
func (a *Agent) ReloadConfig(ctx context.Context, config *agent_config.Config) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// reload the config for the device agent
	if config.LogLevel != "" {
		a.log.Infof("Updating log level to %s", config.LogLevel)
		a.log.Level(config.LogLevel)
	}
	return nil
}
