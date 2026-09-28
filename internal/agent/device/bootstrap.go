package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	grpc_v1 "github.com/flightctl/flightctl/api/grpc/v1"
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
// reconnect the enrollment-hooks gRPC watch on transient failures, and for
// PATCH retries.
var defaultEnrollmentHooksBackoff = wait.Backoff{
	Steps:    10,
	Duration: 5 * time.Second,
	Factor:   2.0,
	Cap:      5 * time.Minute,
}

// enrollmentHooksState is the resolved EnrollmentHooks condition after the
// gRPC watch completes (or from a test override).
type enrollmentHooksState struct {
	conditionAbsent bool
	reason          string
	status          v1beta1.ConditionStatus
	message         string
	condIdx         int
	failurePolicy   v1beta1.FailurePolicyType
	labels          map[string]string
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

	// enrollmentHooksBackoff overrides the default reconnect/PATCH backoff for
	// post-enrollment hooks. Nil means use defaultEnrollmentHooksBackoff.
	enrollmentHooksBackoff *wait.Backoff

	// waitEnrollmentHooksReadyFn overrides the gRPC watch for tests.
	waitEnrollmentHooksReadyFn func(ctx context.Context) (*enrollmentHooksState, error)

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

	if err := b.ensurePostEnrollmentHooks(ctx); err != nil {
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

// ensurePostEnrollmentHooks watches the Device's EnrollmentHooks condition,
// waits for server-side notification if needed, runs OnAfterEnrolling hooks,
// and PATCHes the condition outcome back.
func (b *Bootstrap) ensurePostEnrollmentHooks(ctx context.Context) error {
	state, err := b.waitEnrollmentHooksReady(ctx)
	if err != nil {
		return err
	}

	// No EnrollmentHooks condition means no policy was applied at approval.
	if state.conditionAbsent {
		b.log.Info("No enrollment hooks condition found, proceeding")
		return nil
	}

	switch state.reason {
	case v1beta1.EnrollmentHooksReasonSucceeded,
		v1beta1.EnrollmentHooksReasonContinued,
		v1beta1.EnrollmentHooksReasonManualOverride:
		b.log.Infof("Enrollment hooks condition is %s, proceeding", state.reason)
		return nil

	case v1beta1.EnrollmentHooksReasonFailed:
		// Block path already recorded; do not re-run hooks or start the publisher.
		b.log.Warn("Enrollment hooks condition is Failed, halting")
		return fmt.Errorf("enrollment hooks in Failed state: %w", errEnrollmentHooksFailed)

	case v1beta1.EnrollmentHooksReasonPending:
		// Notify complete (or none configured); run AfterEnrolling below.
	default:
		// Fail closed when the service still gates the device (status False).
		// Known True terminal reasons are handled above; an unknown False
		// reason must not skip hooks or unblock bootstrap.
		if state.status == v1beta1.ConditionStatusFalse {
			b.log.Warnf("Unknown enrollment hooks reason %q with status False, halting", state.reason)
			return fmt.Errorf("enrollment hooks in unknown False state %q: %w", state.reason, errEnrollmentHooksFailed)
		}
		b.log.Warnf("Unknown enrollment hooks reason %q with status %s, proceeding", state.reason, state.status)
		return nil
	}

	enrollCtx, err := b.buildEnrollmentContext(ctx, state)
	if err != nil {
		b.log.Warnf("Failed to build enrollment context: %v", err)
		// Non-fatal: proceed with partial context.
	}

	b.log.Info("Running AfterEnrolling hooks")
	hookErr := b.hookManager.OnAfterEnrolling(ctx, enrollCtx)

	failurePolicy := state.failurePolicy
	if failurePolicy == "" {
		failurePolicy = v1beta1.FailurePolicyBlock
	}
	if hookErr != nil {
		b.log.Warnf("AfterEnrolling hooks failed: %v", hookErr)
		if failurePolicy == v1beta1.FailurePolicyContinue {
			if patchErr := b.patchEnrollmentHooksCondition(ctx, state.condIdx,
				v1beta1.ConditionStatusTrue,
				v1beta1.EnrollmentHooksReasonContinued,
				fmt.Sprintf("hooks failed but policy is Continue: %v", hookErr),
			); patchErr != nil {
				return fmt.Errorf("reporting Continued after hook failure: %w", patchErr)
			}
			return nil
		}
		if patchErr := b.patchEnrollmentHooksCondition(ctx, state.condIdx,
			v1beta1.ConditionStatusFalse,
			v1beta1.EnrollmentHooksReasonFailed,
			fmt.Sprintf("hooks failed: %v", hookErr),
		); patchErr != nil {
			b.log.Warnf("Failed to PATCH Failed enrollment hooks condition: %v", patchErr)
		}
		return fmt.Errorf("post-enrollment hooks failed with Block policy: %w", errEnrollmentHooksFailed)
	}

	if patchErr := b.patchEnrollmentHooksCondition(ctx, state.condIdx,
		v1beta1.ConditionStatusTrue,
		v1beta1.EnrollmentHooksReasonSucceeded,
		"hooks completed successfully",
	); patchErr != nil {
		return fmt.Errorf("reporting Succeeded after hooks: %w", patchErr)
	}
	return nil
}

// waitEnrollmentHooksReady opens a gRPC watch (snapshot + updates) until the
// EnrollmentHooks condition leaves NotifyPending. Retries on transient stream
// errors using the enrollment-hooks backoff.
func (b *Bootstrap) waitEnrollmentHooksReady(ctx context.Context) (*enrollmentHooksState, error) {
	if b.waitEnrollmentHooksReadyFn != nil {
		return b.waitEnrollmentHooksReadyFn(ctx)
	}

	backoff := b.enrollmentBackoff()
	timeout := backoff.Cap
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	watchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result *enrollmentHooksState
	waitErr := wait.ExponentialBackoffWithContext(watchCtx, backoff, func(ctx context.Context) (bool, error) {
		state, err := b.watchEnrollmentHooksOnce(ctx)
		if err != nil {
			b.log.Warnf("Enrollment hooks watch failed (will retry): %v", err)
			return false, nil
		}
		result = state
		return true, nil
	})
	if waitErr != nil {
		if errors.Is(waitErr, context.Canceled) {
			return nil, fmt.Errorf("waiting for enrollment hooks readiness: %w", waitErr)
		}
		if errors.Is(waitErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("waiting for enrollment hooks readiness: %w", errEnrollmentHooksTimeout)
		}
		return nil, fmt.Errorf("waiting for enrollment hooks readiness: %w", errEnrollmentHooksTimeout)
	}
	return result, nil
}

// watchEnrollmentHooksOnce opens one WatchEnrollmentHooks stream and returns
// the terminal event (condition absent or reason != NotifyPending).
func (b *Bootstrap) watchEnrollmentHooksOnce(ctx context.Context) (*enrollmentHooksState, error) {
	enrollmentClient, closer, err := b.identityProvider.CreateEnrollmentGRPCClient(b.managementServiceConfig)
	if err != nil {
		return nil, fmt.Errorf("create enrollment gRPC client: %w", err)
	}
	defer func() {
		if cerr := closer.Close(); cerr != nil {
			b.log.Debugf("closing enrollment gRPC connection: %v", cerr)
		}
	}()

	stream, err := enrollmentClient.WatchEnrollmentHooks(ctx, &grpc_v1.WatchEnrollmentHooksRequest{
		DeviceName: b.deviceName,
	})
	if err != nil {
		return nil, fmt.Errorf("open enrollment hooks watch: %w", err)
	}

	var last *enrollmentHooksState
	for {
		event, recvErr := stream.Recv()
		if recvErr != nil {
			if errors.Is(recvErr, io.EOF) {
				if last != nil && (last.conditionAbsent || last.reason != v1beta1.EnrollmentHooksReasonNotifyPending) {
					return last, nil
				}
				return nil, fmt.Errorf("enrollment hooks watch ended before ready")
			}
			return nil, fmt.Errorf("recv enrollment hooks event: %w", recvErr)
		}
		last = enrollmentHooksStateFromEvent(event)
		if last.conditionAbsent || last.reason != v1beta1.EnrollmentHooksReasonNotifyPending {
			return last, nil
		}
		b.log.Info("Enrollment hooks condition is NotifyPending, waiting for server notification")
	}
}

func enrollmentHooksStateFromEvent(event *grpc_v1.EnrollmentHooksEvent) *enrollmentHooksState {
	if event == nil {
		return &enrollmentHooksState{conditionAbsent: true, condIdx: -1, failurePolicy: v1beta1.FailurePolicyBlock}
	}
	state := &enrollmentHooksState{
		conditionAbsent: event.GetConditionAbsent(),
		reason:          event.GetReason(),
		status:          v1beta1.ConditionStatus(event.GetStatus()),
		message:         event.GetMessage(),
		condIdx:         int(event.GetConditionIndex()),
		failurePolicy:   v1beta1.FailurePolicyType(event.GetFailurePolicy()),
		labels:          event.GetLabels(),
	}
	if state.failurePolicy == "" {
		state.failurePolicy = v1beta1.FailurePolicyBlock
	}
	if state.labels == nil {
		state.labels = make(map[string]string)
	}
	return state
}

// buildEnrollmentContext constructs the EnrollmentContext for OnAfterEnrolling.
func (b *Bootstrap) buildEnrollmentContext(ctx context.Context, state *enrollmentHooksState) (*hook.EnrollmentContext, error) {
	enrollCtx := &hook.EnrollmentContext{
		DeviceName: b.deviceName,
		Labels:     make(map[string]string),
	}

	if state != nil {
		for k, v := range state.labels {
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

// patchEnrollmentHooksCondition sends an RFC 6902 JSON Patch to update the
// EnrollmentHooks condition at the given index. Uses test+replace ops to
// ensure the condition at that index is still the EnrollmentHooks type.
// Retries with the enrollment-hooks backoff on transient failures.
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

	var lastErr error
	backoff := b.enrollmentBackoff()
	err := wait.ExponentialBackoffWithContext(ctx, backoff, func(_ context.Context) (bool, error) {
		if err := b.managementClient.PatchDeviceStatus(ctx, b.deviceName, patch); err != nil {
			lastErr = err
			b.log.Warnf("Failed to PATCH enrollment hooks condition (will retry): %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("patching enrollment hooks condition: %w", err)
		}
		if lastErr != nil {
			return fmt.Errorf("patching enrollment hooks condition: %w", lastErr)
		}
		return fmt.Errorf("patching enrollment hooks condition: %w", err)
	}
	return nil
}
