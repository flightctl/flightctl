package device

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	"github.com/flightctl/flightctl/internal/agent/identity"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/samber/lo"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	BootstrapComplete = "Bootstrap complete"
)

var (
	errEnrollmentHooksFailed  = fmt.Errorf("post-enrollment hooks failed")
	errEnrollmentHooksTimeout = fmt.Errorf("post-enrollment hooks timed out waiting for server notification")
)

// defaultEnrollmentHooksBackoff is the ER-style exponential backoff used to
// poll for enrollment-hook readiness.
var defaultEnrollmentHooksBackoff = wait.Backoff{
	Steps:    10,
	Duration: 5 * time.Second,
	Factor:   2.0,
	Cap:      5 * time.Minute,
}

type Bootstrap struct {
	deviceName        string
	executer          executer.Executer
	deviceReadWriter  fileio.ReadWriter
	specManager       spec.Manager
	statusManager     status.Manager
	hookManager       hook.Manager
	systemInfoManager systeminfo.Manager
	podmanClient      *client.Podman
	systemdClient     *client.Systemd

	lifecycle lifecycle.Initializer

	managementServiceConfig   *baseclient.Config
	managementClient          client.Management
	managementMetricsCallback client.RPCMetricsCallback
	identityProvider          identity.Provider

	// enrollmentHooksBackoff overrides the default polling backoff for
	// post-enrollment hooks. Zero value means use defaultEnrollmentHooksBackoff.
	enrollmentHooksBackoff *wait.Backoff

	log *log.PrefixLogger
}

func NewBootstrap(
	deviceName string,
	executer executer.Executer,
	deviceReadWriter fileio.ReadWriter,
	specManager spec.Manager,
	statusManager status.Manager,
	hookManager hook.Manager,
	lifecycleInitializer lifecycle.Initializer,
	managementServiceConfig *baseclient.Config,
	systemInfoManager systeminfo.Manager,
	managementMetricsCallback client.RPCMetricsCallback,
	podmanClient *client.Podman,
	systemdClient *client.Systemd,
	identityProvider identity.Provider,
	log *log.PrefixLogger,
) *Bootstrap {
	return &Bootstrap{
		deviceName:                deviceName,
		executer:                  executer,
		deviceReadWriter:          deviceReadWriter,
		specManager:               specManager,
		statusManager:             statusManager,
		hookManager:               hookManager,
		lifecycle:                 lifecycleInitializer,
		managementServiceConfig:   managementServiceConfig,
		systemInfoManager:         systemInfoManager,
		managementMetricsCallback: managementMetricsCallback,
		podmanClient:              podmanClient,
		systemdClient:             systemdClient,
		identityProvider:          identityProvider,
		log:                       log,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("Bootstrapping device: %s", b.deviceName)

	var podmanStr string
	podmanVersion, err := b.podmanClient.Version(ctx)
	if err != nil {
		b.log.Error(err)
	} else {
		podmanStr = fmt.Sprintf(", podman-version=%d.%d", podmanVersion.Major, podmanVersion.Minor)
	}

	versionInfo := version.Get()
	b.log.Infof("System information: version=%s, go-version=%s, platform=%s, git-commit=%s%s",
		versionInfo.String(),
		versionInfo.GoVersion,
		versionInfo.Platform,
		versionInfo.GitCommit,
		podmanStr,
	)

	if err := b.ensureSpecFiles(ctx); err != nil {
		return err
	}

	if err := b.ensureEnrollment(ctx); err != nil {
		return err
	}

	if err := b.setManagementClient(); err != nil {
		return err
	}

	if err := b.ensureBootstrap(ctx); err != nil {
		infoMsg := fmt.Sprintf("Bootstrap failed: %v", err)
		_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1beta1.DeviceSummaryStatus{
			Status: v1beta1.DeviceSummaryStatusError,
			Info:   lo.ToPtr(infoMsg),
		}))
		if updateErr != nil {
			b.log.Warnf("Failed setting status: %v", updateErr)
		}
		b.log.Error(infoMsg)

		return err
	}

	b.updateStatus(ctx)

	// Report connectivity status to systemd.
	// This is visible via `systemctl status flightctl-agent` as StatusText.
	if b.systemdClient != nil {
		if err := b.systemdClient.SdNotify(ctx, "STATUS=Connected"); err != nil {
			b.log.Errorf("Failed to notify systemd of connectivity status: %v", err)
		}
	}

	// unset NOTIFY_SOCKET on successful bootstrap to prevent subprocesses from
	// using it.
	// ref: https://bugzilla.redhat.com/show_bug.cgi?id=1781506
	os.Unsetenv("NOTIFY_SOCKET")

	b.log.Info(BootstrapComplete)
	return nil
}

func (b *Bootstrap) ensureEnrollment(ctx context.Context) error {
	err := b.statusManager.Collect(ctx)
	if err != nil {
		b.log.Warnf("Collecting device status: %v", err)
	}

	status := b.statusManager.Get(ctx)
	if status == nil {
		b.log.Warn("Device status is nil, returning default status")
		status = &v1beta1.DeviceStatus{}
	}
	if err := b.lifecycle.Initialize(ctx, status); err != nil {
		return fmt.Errorf("failed to initialize lifecycle: %w", err)
	}

	return nil
}

func (b *Bootstrap) updateStatus(ctx context.Context) {
	updatingCondition := v1beta1.Condition{
		Type: v1beta1.ConditionTypeDeviceUpdating,
	}

	if b.specManager.IsUpgrading() {
		updatingCondition.Status = v1beta1.ConditionStatusTrue
		// TODO: only set rebooting in case where we are actually rebooting
		updatingCondition.Reason = string(v1beta1.UpdateStateRebooting)
	} else {
		rollbackInfo, err := b.specManager.GetRollbackInfo()
		if err != nil {
			b.log.Errorf("Failed to read rollback info: %v", err)
			updatingCondition.Status = v1beta1.ConditionStatusFalse
			updatingCondition.Reason = string(v1beta1.UpdateStateError)
			updatingCondition.Message = "Failed to read rollback info after update"
		} else if rollbackInfo.ErrorMessage != "" || rollbackInfo.Version != "" {
			updatingCondition.Status = v1beta1.ConditionStatusFalse
			updatingCondition.Reason = string(v1beta1.UpdateStateError)
			if rollbackInfo.ErrorMessage != "" {
				updatingCondition.Message = rollbackInfo.ErrorMessage
			} else {
				updatingCondition.Message = fmt.Sprintf("Failed to update to renderedVersion: %s", rollbackInfo.Version)
			}
		} else {
			updatingCondition.Status = v1beta1.ConditionStatusFalse
			updatingCondition.Reason = string(v1beta1.UpdateStateUpdated)
		}
	}

	_, updateErr := b.statusManager.Update(ctx,
		status.SetConfig(v1beta1.DeviceConfigStatus{
			RenderedVersion: b.specManager.RenderedVersion(spec.Current),
		}),
		status.SetCondition(updatingCondition),
	)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}
}

func (b *Bootstrap) ensureSpecFiles(ctx context.Context) error {
	if err := b.specManager.Ensure(); err != nil {
		return fmt.Errorf("ensuring spec files: %w", err)
	}
	return nil
}

func (b *Bootstrap) ensureBootstrap(ctx context.Context) error {
	if err := b.ensureBootedOS(ctx); err != nil {
		return err
	}

	if b.systemInfoManager.IsRebooted() {
		if err := b.hookManager.OnAfterRebooting(ctx); err != nil {
			// TODO: rollback?
			b.log.Errorf("running after rebooting hook: %v", err)
		}
	}

	return nil
}

func (b *Bootstrap) ensureBootedOS(ctx context.Context) error {
	if !b.specManager.ShouldApplyOSImageUpdate() {
		b.log.Info("No OS update in progress")
		// If not upgrading but rollback.json has a desired version, a rollback
		// already completed and the agent restarted. Mark the desired version
		// as failed to prevent re-applying it.
		if !b.specManager.IsUpgrading() {
			rollbackInfo, err := b.specManager.GetRollbackInfo()
			if err != nil {
				b.log.Errorf("Failed to read rollback info: %v", err)
			} else if rollbackInfo.Version != "" {
				b.log.Infof("Marking version %s as failed from previous rollback", rollbackInfo.Version)
				if err := b.specManager.SetUpgradeFailed(rollbackInfo.Version, rollbackInfo.SpecHash); err != nil {
					b.log.Errorf("Failed to mark version %s as failed: %v", rollbackInfo.Version, err)
				}
			}
		}
		return nil
	}

	return b.checkRollback(ctx)
}

func (b *Bootstrap) checkRollback(ctx context.Context) error {
	// check if the bootedOS image is expected
	bootedOS, reconciled, err := b.specManager.CheckOsReconciliation(ctx)
	if err != nil {
		return fmt.Errorf("checking if OS image is reconciled: %w", err)
	}

	if reconciled {
		b.log.Infof("Booted into desired OS image: %s", bootedOS)
		return nil
	}

	desiredOS := b.specManager.OSVersion(spec.Desired)
	// We rebooted without applying the new OS image - something potentially went wrong
	b.log.Warnf("Booted OS image (%s) does not match the desired OS image (%s)", bootedOS, desiredOS)

	_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1beta1.DeviceSummaryStatus{
		Status: v1beta1.DeviceSummaryStatusError,
		Info:   lo.ToPtr(fmt.Sprintf("Booted image %s, expected %s", bootedOS, desiredOS)),
	}))
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	rollback, err := b.specManager.IsRollingBack(ctx)
	if err != nil {
		return fmt.Errorf("checking if rollback is in progress: %w", err)
	}

	if !rollback {
		// this is possible if device was rebooted before new image was applied
		b.log.Warn("No rollback in progress, continuing bootstrap to apply rollback spec")
		return nil
	}

	b.log.Warn("Starting spec rollback")
	// rollback and set the version to failed
	if err := b.specManager.Rollback(ctx, spec.WithSetFailed()); err != nil {
		return fmt.Errorf("failed spec rollback: %w", err)
	}
	b.log.Info("Spec rollback complete, resuming bootstrap")

	updateErr = b.statusManager.UpdateCondition(ctx, v1beta1.Condition{
		Type:    v1beta1.ConditionTypeDeviceUpdating,
		Status:  v1beta1.ConditionStatusTrue,
		Reason:  string(v1beta1.UpdateStateRollingBack),
		Message: fmt.Sprintf("Device is rolling back to template version: %s", b.specManager.RenderedVersion(spec.Desired)),
	})
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (b *Bootstrap) setManagementClient() error {
	var err error
	b.managementClient, err = b.identityProvider.CreateManagementClient(b.managementServiceConfig, b.managementMetricsCallback)
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}

	// initialize the management client for spec and status managers
	b.statusManager.SetClient(b.managementClient)
	b.specManager.SetClient(b.managementClient)
	return nil
}

// ManagementClient returns the management client for use by other components.
func (b *Bootstrap) ManagementClient() client.Management {
	return b.managementClient
}

// enrollmentBackoff returns the configured backoff or the default.
func (b *Bootstrap) enrollmentBackoff() wait.Backoff {
	if b.enrollmentHooksBackoff != nil {
		return *b.enrollmentHooksBackoff
	}
	return defaultEnrollmentHooksBackoff
}

// ensurePostEnrollmentHooks reads the Device's EnrollmentHooks condition,
// waits for server-side notification if needed, runs OnAfterEnrolling hooks,
// and PATCHes the condition outcome back.
func (b *Bootstrap) ensurePostEnrollmentHooks(ctx context.Context) error {
	// Step 1: poll until the condition leaves NotifyPending (or is absent).
	device, condition, condIdx, err := b.pollEnrollmentHooksReady(ctx)
	if err != nil {
		return err
	}

	// D4: nil condition → proceed (no policy).
	if condition == nil {
		b.log.Info("No enrollment hooks condition found, proceeding")
		return nil
	}

	// State machine on the condition reason.
	switch condition.Reason {
	case v1beta1.EnrollmentHooksReasonSucceeded,
		v1beta1.EnrollmentHooksReasonContinued,
		v1beta1.EnrollmentHooksReasonManualOverride:
		// D5/D9: terminal success states → proceed.
		b.log.Infof("Enrollment hooks condition is %s, proceeding", condition.Reason)
		return nil

	case v1beta1.EnrollmentHooksReasonFailed:
		// D8: Failed → skip hooks AND halt.
		b.log.Warn("Enrollment hooks condition is Failed, halting")
		return fmt.Errorf("enrollment hooks in Failed state: %w", errEnrollmentHooksFailed)

	case v1beta1.EnrollmentHooksReasonPending:
		// Continue below to run hooks.
	default:
		b.log.Warnf("Unknown enrollment hooks reason %q, proceeding", condition.Reason)
		return nil
	}

	// Step 2: build EnrollmentContext per D12.
	enrollCtx, err := b.buildEnrollmentContext(ctx, device)
	if err != nil {
		b.log.Warnf("Failed to build enrollment context: %v", err)
		// Non-fatal: proceed with partial context.
	}

	// Step 3: run OnAfterEnrolling hooks.
	b.log.Info("Running AfterEnrolling hooks")
	hookErr := b.hookManager.OnAfterEnrolling(ctx, enrollCtx)

	// Step 4: determine outcome and PATCH.
	failurePolicy := b.getFailurePolicy(device)
	if hookErr != nil {
		b.log.Warnf("AfterEnrolling hooks failed: %v", hookErr)
		if failurePolicy == v1beta1.FailurePolicyContinue {
			// D6: Continued (True) — proceed.
			if patchErr := b.patchEnrollmentHooksCondition(ctx, condIdx,
				v1beta1.ConditionStatusTrue,
				v1beta1.EnrollmentHooksReasonContinued,
				fmt.Sprintf("hooks failed but policy is Continue: %v", hookErr),
			); patchErr != nil {
				b.log.Warnf("Failed to PATCH enrollment hooks condition: %v", patchErr)
			}
			return nil
		}
		// D6: Failed (False) — halt.
		if patchErr := b.patchEnrollmentHooksCondition(ctx, condIdx,
			v1beta1.ConditionStatusFalse,
			v1beta1.EnrollmentHooksReasonFailed,
			fmt.Sprintf("hooks failed: %v", hookErr),
		); patchErr != nil {
			b.log.Warnf("Failed to PATCH enrollment hooks condition: %v", patchErr)
		}
		return fmt.Errorf("post-enrollment hooks failed with Block policy: %w", errEnrollmentHooksFailed)
	}

	// D6: Succeeded (True) — proceed.
	if patchErr := b.patchEnrollmentHooksCondition(ctx, condIdx,
		v1beta1.ConditionStatusTrue,
		v1beta1.EnrollmentHooksReasonSucceeded,
		"hooks completed successfully",
	); patchErr != nil {
		b.log.Warnf("Failed to PATCH enrollment hooks condition: %v", patchErr)
	}
	return nil
}

// pollEnrollmentHooksReady polls GetDevice until the EnrollmentHooks condition
// is no longer NotifyPending. Returns the device, the condition (nil if absent),
// the condition index, and any error.
func (b *Bootstrap) pollEnrollmentHooksReady(ctx context.Context) (*v1beta1.Device, *v1beta1.Condition, int, error) {
	var (
		resultDevice    *v1beta1.Device
		resultCondition *v1beta1.Condition
		resultIdx       int
	)

	backoff := b.enrollmentBackoff()
	pollErr := wait.ExponentialBackoffWithContext(ctx, backoff, func(_ context.Context) (bool, error) {
		device, statusCode, err := b.managementClient.GetDevice(ctx, b.deviceName)
		if err != nil {
			b.log.Warnf("Failed to get device for enrollment hooks: %v", err)
			return false, nil // retry
		}
		if statusCode != http.StatusOK {
			b.log.Warnf("GetDevice returned status %d for enrollment hooks", statusCode)
			return false, nil // retry
		}
		if device == nil || device.Status == nil {
			b.log.Warn("GetDevice returned nil device or status")
			return false, nil // retry
		}

		condition := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceEnrollmentHooks)
		if condition == nil {
			// D4: no condition → done (proceed).
			resultDevice = device
			resultCondition = nil
			resultIdx = -1
			return true, nil
		}

		if condition.Reason == v1beta1.EnrollmentHooksReasonNotifyPending {
			b.log.Info("Enrollment hooks condition is NotifyPending, waiting for server notification")
			return false, nil // keep polling
		}

		// Condition has moved past NotifyPending.
		resultDevice = device
		resultCondition = condition
		for i := range device.Status.Conditions {
			if device.Status.Conditions[i].Type == v1beta1.ConditionTypeDeviceEnrollmentHooks {
				resultIdx = i
				break
			}
		}
		return true, nil
	})

	if pollErr != nil {
		// Backoff exhausted or context cancelled.
		return nil, nil, -1, fmt.Errorf("waiting for enrollment hooks readiness: %w", errEnrollmentHooksTimeout)
	}

	return resultDevice, resultCondition, resultIdx, nil
}

// buildEnrollmentContext constructs the EnrollmentContext for OnAfterEnrolling
// hooks per decision D12.
func (b *Bootstrap) buildEnrollmentContext(ctx context.Context, device *v1beta1.Device) (*hook.EnrollmentContext, error) {
	enrollCtx := &hook.EnrollmentContext{
		DeviceName: b.deviceName,
		Labels:     make(map[string]string),
	}

	// Labels from device.Metadata.Labels.
	if device.Metadata.Labels != nil {
		for k, v := range *device.Metadata.Labels {
			enrollCtx.Labels[k] = v
		}
	}

	// SystemInfo from statusManager.
	deviceStatus := b.statusManager.Get(ctx)
	if deviceStatus != nil {
		sysInfoMap := make(map[string]interface{})
		sysInfoBytes, err := json.Marshal(deviceStatus.SystemInfo)
		if err != nil {
			b.log.Warnf("Failed to marshal system info for post-enrollment hooks: %v", err)
		} else if err := json.Unmarshal(sysInfoBytes, &sysInfoMap); err != nil {
			b.log.Warnf("Failed to unmarshal system info for post-enrollment hooks: %v", err)
		}
		enrollCtx.SystemInfo = sysInfoMap
	}

	// ManagementCertificate from identityProvider.
	certPEM, err := b.identityProvider.GetCertificate()
	if err != nil {
		return enrollCtx, fmt.Errorf("getting management certificate: %w", err)
	}
	certMeta, err := hook.ParseCertMetadata(certPEM)
	if err != nil {
		b.log.Warnf("Failed to parse management certificate metadata: %v", err)
	}
	enrollCtx.ManagementCertificate = certMeta

	return enrollCtx, nil
}

// getFailurePolicy extracts the failure policy from the device's enrollment
// hooks snapshot. Defaults to Block if absent.
func (b *Bootstrap) getFailurePolicy(device *v1beta1.Device) v1beta1.FailurePolicyType {
	if device.Status != nil &&
		device.Status.EnrollmentHooks != nil &&
		device.Status.EnrollmentHooks.Snapshot != nil {
		return device.Status.EnrollmentHooks.Snapshot.FailurePolicy
	}
	return v1beta1.FailurePolicyBlock
}

// patchEnrollmentHooksCondition sends an RFC 6902 JSON Patch to update the
// EnrollmentHooks condition at the given index. Uses test+replace ops to
// ensure the condition at that index is still the EnrollmentHooks type.
func (b *Bootstrap) patchEnrollmentHooksCondition(
	ctx context.Context,
	condIdx int,
	condStatus v1beta1.ConditionStatus,
	reason string,
	message string,
) error {
	basePath := fmt.Sprintf("/status/conditions/%d", condIdx)
	patch := v1beta1.PatchRequest{
		{
			Op:    "test",
			Path:  basePath + "/type",
			Value: string(v1beta1.ConditionTypeDeviceEnrollmentHooks),
		},
		{
			Op:    "replace",
			Path:  basePath + "/status",
			Value: string(condStatus),
		},
		{
			Op:    "replace",
			Path:  basePath + "/reason",
			Value: reason,
		},
		{
			Op:    "replace",
			Path:  basePath + "/message",
			Value: message,
		},
	}
	return b.managementClient.PatchDeviceStatus(ctx, b.deviceName, patch)
}
