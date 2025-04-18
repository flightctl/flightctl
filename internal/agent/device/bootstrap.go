package device

import (
	"context"
	"fmt"
	"os"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	"github.com/flightctl/flightctl/internal/agent/device/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/internal/agent/device/systeminfo"
	baseclient "github.com/flightctl/flightctl/internal/client"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/version"
	"github.com/samber/lo"
)

const (
	BootstrapComplete = "Bootstrap complete"
)

type Bootstrap struct {
	deviceName        string
	executer          executer.Executer
	deviceReadWriter  fileio.ReadWriter
	specManager       spec.Manager
	statusManager     status.Manager
	hookManager       hook.Manager
	systemInfoManager systeminfo.Manager

	lifecycle lifecycle.Initializer

	managementServiceConfig *baseclient.Config
	managementClient        client.Management

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
	log *log.PrefixLogger,
) *Bootstrap {
	return &Bootstrap{
		deviceName:              deviceName,
		executer:                executer,
		deviceReadWriter:        deviceReadWriter,
		specManager:             specManager,
		statusManager:           statusManager,
		hookManager:             hookManager,
		lifecycle:               lifecycleInitializer,
		managementServiceConfig: managementServiceConfig,
		systemInfoManager:       systemInfoManager,
		log:                     log,
	}
}

func (b *Bootstrap) Initialize(ctx context.Context) error {
	b.log.Infof("Bootstrapping device: %s", b.deviceName)
	versionInfo := version.Get()
	b.log.Infof("System information: version=%s, go-version=%s, platform=%s, git-commit=%s",
		versionInfo.String(),
		versionInfo.GoVersion,
		versionInfo.Platform,
		versionInfo.GitCommit,
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
		_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
			Status: v1alpha1.DeviceSummaryStatusError,
			Info:   lo.ToPtr(infoMsg),
		}))
		if updateErr != nil {
			b.log.Warnf("Failed setting status: %v", updateErr)
		}
		b.log.Error(infoMsg)

		return err
	}

	b.updateStatus(ctx)

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
		status = &v1alpha1.DeviceStatus{}
	}
	if err := b.lifecycle.Initialize(ctx, status); err != nil {
		return fmt.Errorf("failed to initialize lifecycle: %w", err)
	}

	return nil
}

func (b *Bootstrap) updateStatus(ctx context.Context) {
	_, updateErr := b.statusManager.Update(ctx, status.SetConfig(v1alpha1.DeviceConfigStatus{
		RenderedVersion: b.specManager.RenderedVersion(spec.Current),
	}))
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	updatingCondition := v1alpha1.Condition{
		Type: v1alpha1.ConditionTypeDeviceUpdating,
	}

	if b.specManager.IsUpgrading() {
		updatingCondition.Status = v1alpha1.ConditionStatusTrue
		// TODO: only set rebooting in case where we are actually rebooting
		updatingCondition.Reason = string(v1alpha1.UpdateStateRebooting)
	} else {
		updatingCondition.Status = v1alpha1.ConditionStatusFalse
		updatingCondition.Reason = string(v1alpha1.UpdateStateUpdated)
	}

	updateErr = b.statusManager.UpdateCondition(ctx, updatingCondition)
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}
}

func (b *Bootstrap) ensureSpecFiles(ctx context.Context) error {
	if b.lifecycle.IsInitialized() {
		// it is unexpected to have a missing spec files when the device is
		// enrolled. reset the spec files to empty if they are missing to allow
		// us to make progress. on the next sync, the device will get the latest
		// desired spec and continue as expected.
		if err := b.specManager.Ensure(); err != nil {
			return fmt.Errorf("resetting spec files: %w", err)
		}
	} else {
		b.log.Info("Device is not enrolled, initializing spec files")
		if err := b.specManager.Initialize(ctx); err != nil {
			return fmt.Errorf("initializing spec files: %w", err)
		}
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
	if !b.specManager.IsOSUpdate() {
		b.log.Info("No OS update in progress")
		// no change in OS image, so nothing else to do here
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

	_, updateErr := b.statusManager.Update(ctx, status.SetDeviceSummary(v1alpha1.DeviceSummaryStatus{
		Status: v1alpha1.DeviceSummaryStatusError,
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

	updateErr = b.statusManager.UpdateCondition(ctx, v1alpha1.Condition{
		Type:    v1alpha1.ConditionTypeDeviceUpdating,
		Status:  v1alpha1.ConditionStatusTrue,
		Reason:  string(v1alpha1.UpdateStateRollingBack),
		Message: fmt.Sprintf("The device is rolling back to template version: %s", b.specManager.RenderedVersion(spec.Desired)),
	})
	if updateErr != nil {
		b.log.Warnf("Failed setting status: %v", updateErr)
	}

	return nil
}

func (b *Bootstrap) setManagementClient() error {
	managementCertExists, err := b.deviceReadWriter.PathExists(b.managementServiceConfig.GetClientCertificatePath())
	if err != nil {
		return fmt.Errorf("generated cert: %q: %w", b.managementServiceConfig.GetClientCertificatePath(), err)
	}

	if !managementCertExists {
		// TODO: we must re-enroll the device in this case
		return fmt.Errorf("management client certificate does not exist")
	}

	// create the management client
	managementHTTPClient, err := client.NewFromConfig(b.managementServiceConfig)
	if err != nil {
		return fmt.Errorf("create management client: %w", err)
	}
	b.managementClient = client.NewManagement(managementHTTPClient)

	// initialize the management client for spec and status managers
	b.statusManager.SetClient(b.managementClient)
	b.specManager.SetClient(b.managementClient)
	b.log.Info("Management client set")
	return nil
}
